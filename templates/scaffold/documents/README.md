# Documents

The texts Barnraise writes for people come from templates: `pr` (the body of
each pull request), `plan` (the structure of plan.md), `item` (an item made
from a proposal) and `issue` (its issue in GitHub or GitLab).

Put a file here to replace the built-in one: `pr.md`, or `pr.<area>.md` and
`pr.<kind>.md` for one area or kind of item. Start from the built-in one:

    barnraise documents customize pr
    barnraise documents preview pr

`documents.diagrams` in barnraise.yaml (auto, always, off) decides when plans
draw the design before and after the change as Mermaid diagrams, which the
pull request then shows. See docs/configuration.md#documents.
