# Slack

prowl reads the PR links people post in your Slack channels and DMs, so a PR someone asked you about in Slack waits in your inbox like a review request. Once you review it, prowl keeps an emoji on the message that shared it in step with your review. On your own PRs, it shows who reacted to the share.

## Setting it up

Slack needs an app to sign in to. Your team needs one; whoever sets prowl up for the team makes it once:

1. Run `prowl slack`. With no app configured yet, it prints the app's manifest.
2. Go to [api.slack.com/apps](https://api.slack.com/apps?new_app=1), choose **Create New App → From a manifest**, pick your workspace and paste the manifest.
3. Copy the app's **Client ID** from **Basic Information → App Credentials** and share it with the team. It isn't a secret.

The manifest asks for user scopes only, so the app has no bot to invite and reads only what you can already read. PKCE is on, which lets prowl sign in with the client ID alone, without the app's secret.

Then everyone signs in, from Slack in the TUI's accounts or from a shell:

```sh
prowl slack
```

Enter the client ID when asked; prowl saves it as `slack_client_id`. prowl opens Slack in your browser, you approve, and prowl keeps your token in the OS keyring (Secret Service on Linux, Keychain on macOS, Credential Manager on Windows). On a machine without a keyring, set `PROWL_SLACK_TOKEN` instead. `prowl slack --off` signs out, and `prowl uninstall --purge` deletes the token too. Slack's token expires after 12 hours, and prowl renews it as it runs. If prowl doesn't run for 30 days, the renewal expires as well and you sign in again the same way.

Prefer your own app? Create it from the same manifest, install it, and paste its user token (`xoxp-...`) at the prompt instead of a client ID.

Last, in prowl's options, under Slack:

- **Channels** lists the channels you're in, the busiest with links to your orgs' PRs first, and recommends the ones with at least 5 in the last two weeks. Check the ones where people ask for reviews. The top row covers every DM and group DM at once.
- **Slack names** matches your teammates' GitHub logins to their Slack users, by email, name, login, and who posts their PRs. Accept the matches, pick someone else, or clear one, then save; they're stored under `[slack_names]`.

prowl reminds you of both after the first sync.

## Shared PRs

Each sync, prowl searches Slack for PR links posted since the last search; the first search looks back a week. A PR linked in a channel you checked, or sent to you in a DM, that you haven't reviewed goes in the **shared** section, right after needs review: "shared in #reviews by bob" or "sent to you by bob". It stays there until you review it or it closes. If you were also asked to review on GitHub, it stays under needs review and the reason says it was shared too. Drafts and your own links don't count.

Slack's search takes about half a minute to find a new message, so a share reaches prowl within a sync of being posted. Slack already notified you, so a new share doesn't send a desktop notification.

## After you review

With `slack_after_review = "react"` (the default), prowl keeps your emoji on the message that shared each PR:

| Emoji | When |
| --- | --- |
| `eyes` | prowl holds an AI review or your inline comments for it |
| `white_check_mark` | you approved |
| `x` | you requested changes |
| `speech_balloon` | you reviewed with comments |

It follows your latest review after the share, wherever you made it, on github.com or in prowl. prowl only removes emoji it added itself. Change them under `[slack_reactions]`.

With `"reply"`, submitting a review from prowl posts its Slack line in the share's thread instead, e.g. `https://github.com/acme/api/pull/12 Approved · @bob · fix: retry on timeout`. With `"off"`, or for a PR nobody shared, the line goes to your clipboard as before.

## Posting and DMs

At any time, after you confirm, prowl can post a PR's Slack line in the thread of the message that shared it (for a PR nobody shared, it copies the line), or send it to the PR's author in a DM. If prowl doesn't know the author's Slack user yet, Slack names opens on them first. A line posted from prowl mentions an author you matched in Slack names; a copied line names them by their Slack display name.

prowl also opens the message that shared a PR. With the Slack desktop app installed, the message opens there; without it, in your browser. On Linux, if Slack only flashes its taskbar icon, remove `-s` from the `Exec=` line of its `slack.desktop` entry: Slack ignores links it's handed with that flag.

The keys are in [Slack keys](tui.md#slack-keys).

## Your PRs

On your own open PRs, prowl reads the reactions on the newest message that shared it, yours or someone else's, and adds them to the reason under Mine: `awaiting reviewers · #reviews: Bob ✅, Carol 👀`. Only the emoji in `[slack_reactions]` count, and yours don't. A change sends a desktop notification.

## What prowl asks Slack for

| Scope | For |
| --- | --- |
| `search:read` | finding PR links |
| `channels:read`, `groups:read`, `im:read` | the channel list |
| `channels:history`, `groups:history`, `im:history` | reading the messages behind the links |
| `reactions:read`, `reactions:write` | your emoji on shares, and reactions on your PRs |
| `chat:write`, `im:write` | posting in threads and DMing an author, only when you confirm |
| `users:read`, `users:read.email` | Slack names |

Everything prowl posts or reacts with is as you. It posts only when you submit a review in reply mode or confirm a post or a DM, and it reacts only with the emoji in `[slack_reactions]`.

Slack limits each app's calls per workspace, and everyone signed in to your team's app shares them. Each sync searches for links (a call per 100 messages, up to 5), reads the reactions on each shared PR of yours, and makes a call to add each emoji it sets and one to remove the one it replaces. The first sync also looks up who you are and the workspace's users.
