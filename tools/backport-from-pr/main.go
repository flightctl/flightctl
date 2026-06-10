package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"

	"github.com/google/go-github/v87/github"
)

func main() {
	log.SetFlags(0)
	var (
		release      string
		owner        string
		repo         string
		remote       string
		keepWorktree bool
	)
	flag.StringVar(&release, "release", "", "Release version")
	flag.StringVar(&owner, "owner", "flightctl", "Github repository owner on which to operate")
	flag.StringVar(&repo, "repo", "flightctl", "Github repository repo on which to operate")
	flag.StringVar(&remote, "remote", "origin", "Local git remote name to pull from to access configured repo")
	flag.BoolVar(&keepWorktree, "keepWorktree", false, "If set, the worktree used to cherry-pick will not be deleted on script exit")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s [FLAGS] <pr number>\n\n", os.Args[0])
		fmt.Fprintln(os.Stderr, "Flags:")
		flag.PrintDefaults()
	}

	flag.Parse()

	if release == "" {
		flag.Usage()
		os.Exit(1)
	}

	prNumbers, err := parseArgsAsPRNumbers()
	if err != nil {
		flag.Usage()
		os.Exit(1)
	}

	expectedReleaseLabel := fmt.Sprintf("release-%s", release)
	releaseBranch := fmt.Sprintf("release-%s", release)
	backportBranch := generateBranchName(prNumbers, release)

	gh, err := github.NewClient(github.WithAuthToken(getCLIAuthToken()))
	if err != nil {
		log.Panicf("Failed to create github client: %v", err)
	}

	var prs []*github.PullRequest
	for _, prNumber := range prNumbers {
		pr, _, err := gh.PullRequests.Get(context.Background(), owner, repo, prNumber)
		if err != nil {
			log.Panicf("Failed to get PR %d from %s/%s: %v", prNumber, owner, repo, err)
		}

		if !pr.GetMerged() {
			log.Panicf("PR %d is not merged, cannot backport yet.", pr.GetNumber())
		}

		if pr.Base.GetRef() != "main" {
			log.Panic("PR is not targeting main branch, cannot backport.")
		}

		if pr.GetMergeCommitSHA() == "" {
			log.Panic("PR does not have a merge commit SHA, is it merged?")
		}

		hasReleaseLabel := slices.ContainsFunc(pr.GetLabels(), func(l *github.Label) bool {
			return l.GetName() == expectedReleaseLabel
		})
		if !hasReleaseLabel {
			log.Panicf("PR does not have the expected release label (%s), are you sure it is meant to be backported to %s", expectedReleaseLabel, release)
		}
		prs = append(prs, pr)
	}

	err = exec.Command("git", "fetch", remote).Run()
	if err != nil {
		log.Panicf("Failed to fetch repo: %v", err)
	}

	worktreeDir, err := os.MkdirTemp(os.TempDir(), "flightctl-backports")
	if err != nil {
		log.Panicf("Failed to create temp dir for worktree: %v", worktreeDir)
	}

	err = exec.Command("git", "worktree", "add", worktreeDir,
		"-B", backportBranch, fmt.Sprintf("%s/%s", remote, releaseBranch)).Run()
	if !keepWorktree {
		defer exec.Command("git", "worktree", "remove", "--force", worktreeDir).Run()
	}
	if err != nil {
		log.Panicf("Failed to create worktree for backport: %v", err)
	}

	git := func(op string, args ...string) (string, error) {
		args = append([]string{"-C", worktreeDir, op}, args...)
		out, err := exec.Command("git", args...).Output()
		return strings.TrimSpace(string(out)), err
	}

	for _, pr := range prs {
		if isCommitAMerge(pr.GetMergeCommitSHA()) {
			log.Print("PR commits were introduced to main via a merge commit.")
			_, err := git("cherry-pick", "-x", "-m", "1", pr.GetMergeCommitSHA())
			if err != nil {
				log.Panicf("Failed to cherry-pick merge commit: %v", err)
			}
		} else {
			log.Print("PR commits were introduced to main via a rebase. Determining which commits to backport.")

			sha, err := git("rev-parse", pr.GetMergeCommitSHA()+"~")
			if err != nil {
				log.Panicf("Failed to get parent of merge commit sha: %v", err)
			}
			commitSHAs := []string{pr.GetMergeCommitSHA()}
			// Walk backwards starting with the parent of the merge commit sha, checking with the GitHub API
			// whether these commits are from the given PR, stopping once a commit that is not part of the PR
			// is reached. This handles rebases with multiple commits as well as squashes where there is only
			// one commit.
			for {
				log.Printf("Checking if commit %s is part of PR %d", sha, pr.GetNumber())
				commitPRs, _, err := gh.PullRequests.ListPullRequestsWithCommit(context.Background(), owner, repo, sha, &github.ListOptions{})
				if err != nil {
					log.Panicf("Failed to lookup PRs for commit %s: %v", sha, err)
					break
				}

				if slices.ContainsFunc(commitPRs, func(commitPR *github.PullRequest) bool {
					return pr.GetNumber() == commitPR.GetNumber()
				}) {
					commitSHAs = append(commitSHAs, sha)
				} else {
					break
				}

				sha, err = git("rev-parse", sha+"~")
				if err != nil {
					log.Panicf("Failed to get parent of commit %s: %v", sha, err)
				}
			}

			slices.Reverse(commitSHAs)
			_, err = git("cherry-pick", append([]string{"-x"}, commitSHAs...)...)
			if err != nil {
				log.Panicf("Failed to cherry pick rebased commit: %v", err)
			}
		}
	}

	_, err = git("push", "-f", remote, backportBranch)
	if err != nil {
		log.Panicf("Failed to push backport branch to remote: %v", err)
	}

	backportPRLabel := fmt.Sprintf("backport-for-%s", release)

	backportPR, _, err := gh.PullRequests.Create(context.Background(), owner, repo, &github.NewPullRequest{
		Title: github.Ptr(generateBackportPRTitle(prs, release)),
		Head:  &backportBranch,
		Base:  &releaseBranch,
		Body:  github.Ptr(generateBackportPRBody(prs, release)),
	})
	if err != nil {
		log.Panicf("Failed to create backport PR: %v", err)
	}

	log.Printf("Created backport PR: %s", backportPR.GetHTMLURL())
	_, _, err = gh.Issues.AddLabelsToIssue(context.Background(), owner, repo, backportPR.GetNumber(), []string{backportPRLabel})
	if err != nil {
		log.Printf("Failed to add backport label to backport PR: %v", err)
	}
}

func getCLIAuthToken() string {
	token, err := exec.Command("gh", "auth", "token").Output()
	if err != nil {
		log.Panic("'gh auth token' failed. Ensure 'gh' cli tool is installed and run 'gh auth login' first.")
	}
	return strings.TrimSpace(string(token))
}

func isCommitAMerge(sha string) bool {
	out, _ := exec.Command("git", "show", "--summary", sha).Output()
	return strings.Contains(string(out), "Merge:")
}

func parseArgsAsPRNumbers() ([]int, error) {
	var out []int
	if flag.NArg() == 0 {
		return nil, errors.New("no PR numbers specified")
	}
	for _, a := range flag.Args() {
		num, err := strconv.Atoi(a)
		if err != nil {
			return nil, err
		}
		out = append(out, num)
	}
	return out, nil
}

func generateBranchName(prNumbers []int, release string) string {
	if len(prNumbers) == 1 {
		return fmt.Sprintf("backport-pr-%d-for-%s", prNumbers[0], release)
	}
	return fmt.Sprintf("backport-prs-%d-to-%d-for-%s", prNumbers[0], prNumbers[len(prNumbers)-1], release)
}

func generateBackportPRTitle(prs []*github.PullRequest, release string) string {
	if len(prs) == 1 {
		return fmt.Sprintf("[Backport to %s] %s", release, prs[0].GetTitle())
	}
	return fmt.Sprintf("[Backport to %s] Mass backport from PR %d to %d", release, prs[0].GetNumber(), prs[len(prs)-1].GetNumber())
}

func generateBackportPRBody(prs []*github.PullRequest, release string) string {
	var out strings.Builder

	fmt.Fprintf(&out, "*Backporting PRs for release %s:*\n\n", release)
	for _, pr := range prs {
		fmt.Fprintf(&out, "PR *[%s](%s)*\n", pr.GetTitle(), pr.GetHTMLURL())
	}
	return out.String()
}
