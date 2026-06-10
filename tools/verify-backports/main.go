package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/google/go-github/v87/github"
)

const (
	owner = "flightctl"
	repo  = "flightctl"
)

func main() {
	log.SetFlags(0)
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s <release version>\n\n", os.Args[0])
	}
	flag.Parse()

	release := flag.Arg(0)
	if release == "" {
		flag.Usage()
		os.Exit(1)
	}

	releaseLabel := fmt.Sprintf("release-%s", release)
	releaseBranch := "origin/" + releaseLabel
	backportLabel := fmt.Sprintf("backport-for-%s", release)

	gh, err := github.NewClient(github.WithAuthToken(getCLIAuthToken()))
	if err != nil {
		log.Panicf("Failed to create github client: %v", err)
	}

	prIter := gh.PullRequests.ListIter(context.Background(), owner, repo, &github.PullRequestListOptions{
		State:     "all",
		Base:      "main",
		Sort:      "updated",
		Direction: "desc",
	})

	since := time.Now().AddDate(0, -4, 0)
	log.Printf("Querying all PRs updated since %s", since.Format(time.DateOnly))

	var openPRs []*github.PullRequest
	var mergedPRs []*github.PullRequest
	for pr, err := range prIter {
		if err != nil {
			log.Panicf("Failed to iterate PRs: %v", err)
		}

		if pr.GetUpdatedAt().Time.Before(since) {
			break
		}

		if !slices.ContainsFunc(pr.GetLabels(), func(l *github.Label) bool {
			return l.GetName() == releaseLabel
		}) {
			continue
		}

		if pr.GetState() == "open" {
			openPRs = append(openPRs, pr)
		} else if pr.GetMergeCommitSHA() != "" && !pr.GetMergedAt().IsZero() && pr.GetState() == "closed" {
			mergedPRs = append(mergedPRs, pr)
		}
	}

	if len(openPRs) > 0 {
		log.Printf("Open PRs for %s", release)
		log.Printf("===============")
		for _, pr := range openPRs {
			log.Printf("#%5d | %25s | %s | %s", pr.GetNumber(), pr.GetTitle(), pr.GetHTMLURL(), pr.GetUser().GetLogin())
		}
		log.Print("")
	}

	git := func(args ...string) (string, error) {
		out, err := exec.Command("git", args...).Output()
		return strings.TrimSpace(string(out)), err
	}

	_, err = git("fetch", "origin")
	if err != nil {
		log.Panicf("Failed to fetch origin: %v", err)
	}

	log.Printf("Checking %d PRs for backport status", len(mergedPRs))

	var missingPRs []*github.PullRequest
	for _, pr := range mergedPRs {
		mainSHA := pr.GetMergeCommitSHA()

		_, err := git("merge-base", "--is-ancestor", mainSHA, releaseBranch)
		if err == nil {
			// Commit exists in branch
			continue
		}

		out, _ := git("log", "--grep", "cherry picked from commit "+mainSHA, releaseBranch)
		if len(out) > 0 {
			// Backported via cherry-pick
			continue
		}

		missingPRs = append(missingPRs, pr)
	}
	log.Print("")

	if len(missingPRs) > 0 {
		log.Printf("PRs missing from %s", release)
		log.Printf("===================")
		for _, pr := range missingPRs {
			log.Printf("#%5d | %25s | %s | %s", pr.GetNumber(), pr.GetTitle(), pr.GetHTMLURL(), pr.GetUser().GetLogin())
		}
	} else {
		log.Print("All PRs are backported")
	}
	log.Print("")

	backportPRIter := gh.Issues.ListByRepoIter(context.Background(), owner, repo, &github.IssueListByRepoOptions{
		State:  "open",
		Labels: []string{backportLabel},
	})
	log.Print("Open Backport PRs")
	log.Print("=================")
	for issue, err := range backportPRIter {
		if err != nil {
			log.Panicf("Failed to get backport PRs: %v", err)
		}
		log.Printf("%30s | %s | %s", issue.GetTitle(), issue.GetHTMLURL(), issue.GetUser().GetLogin())
	}
}

func getCLIAuthToken() string {
	token, err := exec.Command("gh", "auth", "token").Output()
	if err != nil {
		log.Panic("'gh auth token' failed. Ensure 'gh' cli tool is installed and run 'gh auth login' first.")
	}
	return strings.TrimSpace(string(token))
}
