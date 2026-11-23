package github

import (
	"context"
	"fmt"

	"github.com/leandrodaf/barnraise/internal/adapters/out/httpx/itemdoc"
)

type projectOption struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type projectInfo struct {
	ID      string
	FieldID string
	Options []projectOption
}

const projectQuery = `query($owner: String!, $number: Int!) {
  repositoryOwner(login: $owner) {
    ... on ProjectV2Owner {
      projectV2(number: $number) {
        id
        title
        field(name: "Status") {
          ... on ProjectV2SingleSelectField { id name options { id name } }
        }
      }
    }
  }
}`

// projectInfo finds the project, its Status field and options (cached).
func (c *Client) projectInfo(ctx context.Context) (*projectInfo, error) {
	c.mu.Lock()
	p := c.project
	c.mu.Unlock()
	if p != nil {
		return p, nil
	}
	var data struct {
		RepositoryOwner *struct {
			ProjectV2 *struct {
				ID    string `json:"id"`
				Field *struct {
					ID      string          `json:"id"`
					Options []projectOption `json:"options"`
				} `json:"field"`
			} `json:"projectV2"`
		} `json:"repositoryOwner"`
	}
	err := c.graphql(ctx, projectQuery, map[string]any{"owner": c.opts.Owner, "number": c.opts.Project}, &data)
	if err != nil {
		return nil, err
	}
	if data.RepositoryOwner == nil || data.RepositoryOwner.ProjectV2 == nil {
		return nil, fmt.Errorf("github: project %d of %s not found", c.opts.Project, c.opts.Owner)
	}
	pv := data.RepositoryOwner.ProjectV2
	if pv.Field == nil || pv.Field.ID == "" {
		return nil, fmt.Errorf("github: project %d has no single-select field named Status", c.opts.Project)
	}
	p = &projectInfo{ID: pv.ID, FieldID: pv.Field.ID, Options: pv.Field.Options}
	c.mu.Lock()
	c.project = p
	c.mu.Unlock()
	return p, nil
}

// optionFor picks the Status option for a Barnraise status. ok is false when
// the status is not mapped and no option has a matching name.
func (c *Client) optionFor(p *projectInfo, status string) (projectOption, bool, error) {
	if name, mapped := c.opts.Columns[status]; mapped {
		for _, o := range p.Options {
			if itemdoc.SameName(o.Name, name) {
				return o, true, nil
			}
		}
		return projectOption{}, false, fmt.Errorf("github: project %d has no Status option %q (mapped from %s)", c.opts.Project, name, status)
	}
	for _, o := range p.Options {
		if itemdoc.SameName(o.Name, status) {
			return o, true, nil
		}
	}
	return projectOption{}, false, nil
}

const issueItemsQuery = `query($owner: String!, $name: String!, $number: Int!) {
  repository(owner: $owner, name: $name) {
    issue(number: $number) {
      id
      projectItems(first: 100) { nodes { id project { id } } }
    }
  }
}`

const addItemMutation = `mutation($project: ID!, $content: ID!) {
  addProjectV2ItemById(input: {projectId: $project, contentId: $content}) { item { id } }
}`

const setStatusMutation = `mutation($project: ID!, $item: ID!, $field: ID!, $option: String!) {
  updateProjectV2ItemFieldValue(input: {projectId: $project, itemId: $item, fieldId: $field, value: {singleSelectOptionId: $option}}) {
    projectV2Item { id }
  }
}`

// setProjectStatus puts the issue on the project (adding it when missing)
// and sets its Status. A status with no mapping and no same-named option is
// left alone.
func (c *Client) setProjectStatus(ctx context.Context, number int, status string) error {
	p, err := c.projectInfo(ctx)
	if err != nil {
		return err
	}
	opt, ok, err := c.optionFor(p, status)
	if err != nil || !ok {
		return err
	}
	var data struct {
		Repository *struct {
			Issue *struct {
				ID           string `json:"id"`
				ProjectItems struct {
					Nodes []struct {
						ID      string `json:"id"`
						Project struct {
							ID string `json:"id"`
						} `json:"project"`
					} `json:"nodes"`
				} `json:"projectItems"`
			} `json:"issue"`
		} `json:"repository"`
	}
	if err := c.graphql(ctx, issueItemsQuery, map[string]any{"owner": c.owner, "name": c.repo, "number": number}, &data); err != nil {
		return err
	}
	if data.Repository == nil || data.Repository.Issue == nil {
		return fmt.Errorf("github: issue #%d not found", number)
	}
	issue := data.Repository.Issue
	itemID := ""
	for _, n := range issue.ProjectItems.Nodes {
		if n.Project.ID == p.ID {
			itemID = n.ID
			break
		}
	}
	if itemID == "" {
		var added struct {
			AddProjectV2ItemByID struct {
				Item struct {
					ID string `json:"id"`
				} `json:"item"`
			} `json:"addProjectV2ItemById"`
		}
		if err := c.graphql(ctx, addItemMutation, map[string]any{"project": p.ID, "content": issue.ID}, &added); err != nil {
			return err
		}
		itemID = added.AddProjectV2ItemByID.Item.ID
		if itemID == "" {
			return fmt.Errorf("github: adding issue #%d to project %d returned no item", number, c.opts.Project)
		}
	}
	return c.graphql(ctx, setStatusMutation, map[string]any{
		"project": p.ID, "item": itemID, "field": p.FieldID, "option": opt.ID,
	}, nil)
}
