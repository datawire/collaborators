package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
)

type graphqlRequest struct {
	Query     string                 `json:"query"`
	Variables map[string]interface{} `json:"variables"`
}

type graphqlResponse struct {
	Data   json.RawMessage `json:"data"`
	Errors []interface{}   `json:"errors"`
}

func graphql(out interface{}, query string, arguments map[string]interface{}) error {
	reqbody, err := json.Marshal(graphqlRequest{Query: query, Variables: arguments})
	if err != nil {
		return err
	}
	httpreq, err := http.NewRequest(http.MethodPost, "https://api.github.com/graphql", bytes.NewReader(reqbody))
	if err != nil {
		return err
	}
	httpreq.Header.Add("Authorization", "bearer "+os.Getenv("GH_TOKEN"))

	httpresp, err := http.DefaultClient.Do(httpreq)
	if err != nil {
		return err
	}
	defer httpreq.Body.Close()

	respbody, err := ioutil.ReadAll(httpresp.Body)
	if err != nil {
		return err
	}
	var gqlresp graphqlResponse
	if err := json.Unmarshal(respbody, &gqlresp); err != nil {
		return err
	}
	if len(gqlresp.Errors) > 0 {
		return fmt.Errorf("graphql error: %v", gqlresp.Errors)
	}
	return json.Unmarshal(gqlresp.Data, &out)
}

type Permission int

const (
	PermNONE = iota
	PermREAD
	PermWRITE
	PermADMIN
)

func (p *Permission) UnmarshalText(text []byte) error {
	val, ok := map[string]Permission{
		"NONE":  PermNONE,
		"READ":  PermREAD,
		"WRITE": PermWRITE,
		"ADMIN": PermADMIN,
	}[string(text)]
	if !ok {
		return fmt.Errorf("invalid permission enum string: %q", text)
	}
	*p = val
	return nil
}

func (p Permission) String() string {
	val, ok := map[Permission]string{
		PermNONE:  "NONE",
		PermREAD:  "READ",
		PermWRITE: "WRITE",
		PermADMIN: "ADMIN",
	}[p]
	if !ok {
		return fmt.Sprintf("Permission(%d)", p)
	}
	return val
}

func getTeamFullnames(orgname string) (map[string]string, error) {
	query := `
query($orgname: String!, $cursor: String) {
  organization(login: $orgname) {
    teams(first: 100, after: $cursor) {
      pageInfo {
        hasNextPage
        endCursor
      }
      nodes {
        slug
        parentTeam {
          slug
        }
      }
    }
  }
}`
	var rawTeams struct {
		Organization struct {
			Teams struct {
				PageInfo struct {
					HasNextPage bool
					EndCursor   string
				}
				Nodes []struct {
					Slug       string
					ParentTeam *struct {
						Slug string
					}
				}
			}
		}
	}
	args := map[string]interface{}{
		"orgname": orgname,
	}
	var teamSlugs []string
	teamParents := make(map[string]string)
	for args["cursor"] == nil || rawTeams.Organization.Teams.PageInfo.HasNextPage {
		err := graphql(&rawTeams, query, args)
		if err != nil {
			return nil, err
		}
		args["cursor"] = rawTeams.Organization.Teams.PageInfo.EndCursor

		for _, teamInfo := range rawTeams.Organization.Teams.Nodes {
			teamSlugs = append(teamSlugs, teamInfo.Slug)
			if teamInfo.ParentTeam != nil {
				teamParents[teamInfo.Slug] = teamInfo.ParentTeam.Slug
			}
		}
	}

	teamFullnames := make(map[string]string, len(teamSlugs))
	for _, teamSlug := range teamSlugs {
		full := teamSlug
		tip := teamSlug
		for tip != "" {
			parent := teamParents[tip]
			if parent != "" {
				full = parent + "/" + full
			}
			tip = parent
		}
		teamFullnames[teamSlug] = full
	}

	return teamFullnames, nil
}

func getCollaborators(teamFullnames map[string]string, orgname, reponame string) (map[string]Permission, error) {
	var rawRepo struct {
		Organization struct {
			Repository struct {
				Collaborators struct {
					Edges []struct {
						Node struct {
							Login string
						}
						PermissionSources []struct {
							Permission Permission
							Source     struct {
								Org  string
								Repo string
								Team string
							}
						}
					}
				}
			}
		}
	}
	err := graphql(&rawRepo, `
query($orgname: String!, $reponame: String!) {
  organization(login: $orgname) {
    repository(name: $reponame) {
      collaborators {
        edges {
          node {
            login
          }
          permissionSources {
            permission
            source {
              ... on Organization {
                org: login
              }
              ... on Repository {
                repo: name
              }
              ... on Team {
                team: slug
              }
            }
          }
        }
      }
    }
  }
}`, map[string]interface{}{
		"orgname":  orgname,
		"reponame": reponame,
	})
	if err != nil {
		return nil, err
	}
	ret := map[string]Permission{}
	for _, userInfo := range rawRepo.Organization.Repository.Collaborators.Edges {
		isOrgOwner := false
		skippedSources := make(map[string]bool)
		for _, source := range userInfo.PermissionSources {
			var key string
			switch {
			case source.Source.Org != "":
				key = "org:" + source.Source.Org
			case source.Source.Team != "":
				key = "team:" + teamFullnames[source.Source.Team]
			case source.Source.Repo != "":
				key = "user:" + userInfo.Node.Login
			}
			if key == "org:"+orgname {
				if source.Permission == PermADMIN {
					isOrgOwner = true
				}
				// Don't bother recording this in to `ret`; of course the org that a repo is in has
				// access to that repo.
				continue
			}
			if isOrgOwner && !skippedSources[key] && source.Permission == PermADMIN {
				// If the user is an organization owner, then the API makes it look like they also have
				// ADMIN on each and every specific repo for a bunch of other specific reasons.  Remove
				// this duplication.
				skippedSources[key] = true
				continue
			}
			if val, exists := ret[key]; exists && val != source.Permission {
				if strings.HasPrefix(key, "team:") && (val == PermWRITE && source.Permission == PermADMIN) || (val == PermADMIN && source.Permission == PermWRITE) {
					// IDK, the API sometimes spits out a duplicate "WRITE" for teams that have "ADMIN"?
					ret[key] = PermADMIN
					continue
				}
				return nil, fmt.Errorf("mismatch for reponame=%q collaborator=%q : %q != %q",
					reponame, key, val, source.Permission)
			}
			ret[key] = source.Permission
		}
	}
	return ret, nil
}

type RepoHandle struct {
	Name string
	URL  string
}

func getRepos(orgname string) ([]RepoHandle, error) {
	query := `					
query($orgname: String!, $cursor: String) {
  organization(login: $orgname) {
    repositories(first: 100, after: $cursor, orderBy: {field: UPDATED_AT, direction: DESC}) {
      pageInfo {
        hasNextPage
        endCursor
      }
      nodes {
        name
        url
        isArchived
      }
    }
  }
}`
	var rawRepos struct {
		Organization struct {
			Repositories struct {
				PageInfo struct {
					HasNextPage bool
					EndCursor   string
				}
				Nodes []struct {
					Name       string
					URL        string
					IsArchived bool
				}
			}
		}
	}
	args := map[string]interface{}{
		"orgname": orgname,
	}
	var repos []RepoHandle
	for args["cursor"] == nil || rawRepos.Organization.Repositories.PageInfo.HasNextPage {
		err := graphql(&rawRepos, query, args)
		if err != nil {
			return nil, err
		}
		args["cursor"] = rawRepos.Organization.Repositories.PageInfo.EndCursor

		for _, repoInfo := range rawRepos.Organization.Repositories.Nodes {
			if repoInfo.IsArchived {
				continue
			}
			repos = append(repos, RepoHandle{Name: repoInfo.Name, URL: repoInfo.URL})
		}
	}
	return repos, nil
}

func Main(orgname string) error {
	if os.Getenv("GH_TOKEN") == "" {
		return fmt.Errorf("must set the GH_TOKEN environment variable to a GitHub personal access token that has the 'admin:org' permission")
	}
	teamFullnames, err := getTeamFullnames(orgname)
	if err != nil {
		return err
	}
	repos, err := getRepos(orgname)
	if err != nil {
		return err
	}
	output := tabwriter.NewWriter(os.Stdout, 0, 8, 2, ' ', 0)
	for i, repo := range repos {
		fmt.Fprintf(os.Stderr, "inspecting repo %d/%d %q\n", i, len(repos), repo.Name)
		collaborators, err := getCollaborators(teamFullnames, orgname, repo.Name)
		if err != nil {
			return fmt.Errorf("%s: %w", repo.URL, err)
		}
		bucketNames := []string{"org", "team", "user"}
		buckets := make(map[string][]string, len(bucketNames))
		for _, bucketName := range bucketNames {
			for k, v := range collaborators {
				if strings.HasPrefix(k, bucketName+":") {
					buckets[bucketName] = append(buckets[bucketName], k+"="+v.String())
				}
			}
		}
		fmt.Fprintf(output, "%s", repo.URL)
		for _, bucketName := range bucketNames {
			items := buckets[bucketName]
			sort.Strings(items)
			fmt.Fprintf(output, "\t%s", strings.Join(items, " "))
		}
		fmt.Fprintf(output, "\n")
	}
	output.Flush()

	return nil
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "Usage: %s orgname\n", os.Args[0])
		os.Exit(2)
	}
	if err := Main(os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
