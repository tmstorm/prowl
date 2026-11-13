# Security

Report a vulnerability privately: on the repository's Security tab, choose "Report a vulnerability". Please don't open a public issue for it. Fixes go into the latest release only.

prowl runs AI agents on pull requests written by other people, so a way for a PR to make the review agent run a command, write a file or reach the network counts as a vulnerability. See [AI reviews](docs/ai-reviews.md) for what the agent may do.

## Checking a download

Each release's `checksums.txt` is signed with prowl's release key, [`minisign.pub`](minisign.pub). `prowl update` checks it before it installs anything. To check a download yourself:

```sh
minisign -Vm checksums.txt -p minisign.pub
sha256sum --check --ignore-missing checksums.txt
```
