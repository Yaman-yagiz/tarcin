# Security policy

## Reporting a vulnerability

**Do not open a public issue for security problems.**

Report vulnerabilities privately through GitHub's
**Security → Report a vulnerability** form on this repository. If that is
unavailable, contact the maintainer listed in the GitHub profile of
[@Yaman-yagiz](https://github.com/Yaman-yagiz) and ask for a private
channel.

Please include:

- the affected version or commit
- a description of the issue and its impact
- steps to reproduce, or a proof of concept
- any suggested fix

tarcin is maintained by volunteers, so response times are best effort.
We aim to acknowledge a report within 7 days and to agree a disclosure
timeline with you. Reporters are credited unless they prefer otherwise.

## Scope

In scope:

- Terminal escape or control-sequence injection through message content,
  names or file names
- Path traversal or unsafe file handling for media
- Leaks of message content, keys or phone numbers into logs, crash output
  or files with weak permissions
- Command injection through external programs (`$EDITOR`, openers,
  notifications)
- Supply-chain issues in our build or release process

Out of scope (please report these upstream instead):

- Weaknesses in the WhatsApp protocol itself, or in whatsmeow. Report
  them to [whatsmeow](https://github.com/tulir/whatsmeow).
- Attacks that require malware already running as the same user (see the
  [threat model](docs/architecture/security.md#2-adversaries-and-scope)).
- Account restrictions imposed by WhatsApp.

## Supported versions

Before `v1.0.0`, only the latest release and `main` receive security
fixes.
