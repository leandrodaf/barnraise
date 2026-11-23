# Barnraise check action

Reports the result of a Barnraise check from GitHub Actions to the daemon's API (`ReportCheck`, scope `checks:report`). Green moves the item on; red sends it back to the implementer as rework with the summary.

## How it fits

For a check declared with `runs_on: github-actions`:

```yaml
# barnraise.yaml
checks:
  unit:
    runs_on: github-actions
    workflow: ci.yml
```

Barnraise emits `check.requested` and dispatches the workflow (`workflow_dispatch`) on the item's branch with three inputs: `item`, `commit` and `check`. The workflow runs on GitHub's runners and reports back with this action.

## Setup

1. Create a token that can only report checks for the project:

   ```sh
   barnraise token create -p demo --name github-ci --scope checks:report --ttl 90d
   ```

2. Store it as the repository secret `BARNRAISE_TOKEN`, and the API URL as `BARNRAISE_URL` (the daemon must be reachable from GitHub over TLS, for example behind a reverse proxy).

3. Accept the dispatch inputs and report at the end:

```yaml
name: ci
on:
  push:
  pull_request:
  workflow_dispatch:
    inputs:
      item:
        description: Barnraise item (I-42)
        required: true
      commit:
        description: Commit to check
        required: true
      check:
        description: Barnraise check name
        required: true

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          ref: ${{ inputs.commit || github.sha }}
      - name: tests
        id: tests
        run: make test

      - name: report to Barnraise
        if: always() && github.event_name == 'workflow_dispatch'
        uses: leandrodaf/barnraise/action/check@main
        with:
          url: ${{ secrets.BARNRAISE_URL }}
          token: ${{ secrets.BARNRAISE_TOKEN }}
          project: demo
          item: ${{ inputs.item }}
          check: ${{ inputs.check }}
          commit: ${{ inputs.commit }}
          passed: ${{ steps.tests.outcome == 'success' }}
          summary: "make test: ${{ steps.tests.outcome }} (${{ github.server_url }}/${{ github.repository }}/actions/runs/${{ github.run_id }})"
```

## Inputs

| Input | Required | Meaning |
| --- | --- | --- |
| `url` | yes | Base URL of the Barnraise API |
| `token` | yes | Token with `checks:report` |
| `project` | yes | Project name |
| `item` | yes | Item id, from the dispatch input `item` |
| `check` | yes | Check name, from the dispatch input `check` |
| `passed` | yes | `true` when the check passed |
| `summary` | no | One line about the result |
| `commit` | no | Commit checked; defaults to `github.sha` |

The action sends:

```json
{"project": "demo", "result": {"item_id": "I-42", "check": "unit", "passed": true, "summary": "…", "commit": "<sha>"}}
```

to `POST <url>/barnraise.v1.BarnraiseService/ReportCheck` with `Authorization: Bearer <token>`, and fails the step when the API does not accept it.

Without this action, Barnraise can also read the result from the GitHub webhook (`workflow_run` completed on a `br/I-<n>-…` branch), when the webhook is configured for the repository. See [docs/api.md](../../docs/api.md#github-actions-checks).
