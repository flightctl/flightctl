Flight Control Contributor Policy

Welcome!
Thank you for considering a contribution to Flight Control. We appreciate your time and effort in helping us improve this project. This document explains how to contribute effectively and responsibly, following CNCF Best Practices and the Contributor Covenant v2.1 to maintain a welcoming, open source community.

1. Before You Get Started

GitHub Account  
You’ll need a GitHub account with a verified email address.  
- How to verify: https://docs.github.com/en/account-and-profile/email-visibility/about-email-verification

Developer Certificate of Origin (DCO) & Commit Signing  
All commits must be:
- Signed off using the -s flag (Developer Certificate of Origin)
- Cryptographically signed with your GitHub SSH key using the -S flag

Example:
git commit -S -s -m "NO-ISSUE: Add new feature"

For configuration details on commit signing, see:  
https://docs.github.com/en/authentication/managing-commit-signature-verification/telling-git-about-your-signing-key

Commit Message Format  
If you don’t have a Jira issue number, start your commit message with:
NO-ISSUE: Your summary here

Code of Conduct  
By participating, you agree to uphold our Code of Conduct.  
If you experience or witness any violations, contact: conduct@flightcontrol.io (replace with a real address)

2. What Can I Contribute?

Flight Control welcomes all contributions:
- Code: New features, bug fixes, performance improvements
- Documentation: Tutorials, API references, FAQs
- Tests & CI: Unit tests, integration tests, pipelines
- Design & UX: Visual assets, user flows, wireframes
- Community: Issue triage, Q&A, helping other contributors

Tip: Look for issues tagged “good first issue” to help newcomers get started.

3. Contribution Process

Fork & Clone  
Fork the repository on GitHub, then clone it locally.

Example:
git clone https://github.com/<YOUR_USERNAME>/flightctl.git
cd flightctl

Create a Branch  
Name your branch descriptively, e.g., feature/add-new-thing or fix/docs-typo.

Example:
git checkout -b feature/add-new-thing

Make & Test Changes  
Implement your idea or fix, ensuring you follow coding standards and add or update tests as needed.

Commit with DCO Sign-Off and Signature  
Use the following command to commit your changes:
git commit -S -s -m "NO-ISSUE: Brief summary of change"

Push & Open a PR  
Push your branch:
git push origin feature/add-new-thing

Then open a Pull Request (PR) against the main branch. In your PR, provide a clear description of:
- What you changed
- Why you changed it
- How you tested your changes

Code Review  
A project maintainer will review your PR and may request changes or clarifications. Please be patient and responsive.

Merge  
Once your PR is approved and all checks pass, it will be merged.

4. Code & Style Guidelines

- Write clear, maintainable, and well-commented code.
- Update or add tests if your changes affect functionality.
- Use short, descriptive commit messages.
- Update documentation (including the README) if your changes introduce new concepts or features.
- Follow any project-specific linting or formatting rules (we’ll publish a detailed Coding Style Guide soon).

5. Licensing & Legal

Open Source License  
By contributing, you agree to license your work under the same terms as this project’s primary license (see the LICENSE file).

DCO Compliance  
Every commit must be signed off (-s) and cryptographically signed (-S) to confirm you accept the Developer Certificate of Origin.

No CLA Needed (for now)  
We do not currently require a Contributor License Agreement (CLA). If that changes, contributors will be notified.

6. Code of Conduct (Detailed)

We follow the Contributor Covenant v2.1. By participating, you agree to treat others with respect and kindness.  
Enforcement:  
- If you spot or experience any misconduct, please reach out confidentially at conduct@flightcontrol.io.
- Our maintainers will review and address each case promptly and fairly.

7. Contributor Recognition

We celebrate contributions! Contributors will be recognized in:
- A CONTRIBUTORS.md file in the repo
- Release notes for significant features or fixes
- The official project website (under development)

8. CNCF Alignment

Flight Control aligns with CNCF Open Source Best Practices:
- Transparent Governance: Public decisions and open meeting invites.
- Inclusive Communication: Use of Slack, forums, or mailing lists for open discussion.
- Onboarding Support: “Good first issue” tags, clear documentation, and friendly reviews.
- Legal Clarity: Signed DCO and an OSI-approved license.

9. Questions & Support

- GitHub Discussions: (Coming soon) Post your questions or ideas.
- Slack/Chat: (Future workspace link) Join our real-time conversation channel.
- GitHub Issues: Open an issue if you find a bug or need a new feature.

Thank you for contributing to Flight Control!
