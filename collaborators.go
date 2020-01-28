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

func getTeamFullnames() (map[string]string, error) {
	var rawTeams struct {
		Organization struct {
			Teams struct {
				Nodes []struct {
					Slug       string
					ParentTeam *struct {
						Slug string
					}
				}
			}
		}
	}
	err := graphql(&rawTeams, `
query {
  organization(login: "datawire") {
    teams(first: 100) {
      nodes {
        slug
        parentTeam {
          slug
        }
      }
    }
  }
}
`, nil)
	if err != nil {
		return nil, err
	}
	teamParents := make(map[string]string)
	for _, teamInfo := range rawTeams.Organization.Teams.Nodes {
		if teamInfo.ParentTeam != nil {
			teamParents[teamInfo.Slug] = teamInfo.ParentTeam.Slug
		}
	}
	teamFullnames := make(map[string]string, len(rawTeams.Organization.Teams.Nodes))
	for _, teamInfo := range rawTeams.Organization.Teams.Nodes {
		full := teamInfo.Slug
		tip := teamInfo.Slug
		for tip != "" {
			parent := teamParents[tip]
			if parent != "" {
				full = parent + "/" + full
			}
			tip = parent
		}
		teamFullnames[teamInfo.Slug] = full
	}

	return teamFullnames, nil
}

func getCollaborators(teamFullnames map[string]string, reponame string) (map[string]string, error) {
	var rawRepo struct {
		Organization struct {
			Repository struct {
				Collaborators struct {
					Edges []struct {
						Node struct {
							Login string
						}
						PermissionSources []struct {
							Permission string
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
query($reponame: String!) {
  organization(login: "datawire") {
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
		"reponame": reponame,
	})
	if err != nil {
		return nil, err
	}
	ret := map[string]string{}
	for _, userInfo := range rawRepo.Organization.Repository.Collaborators.Edges {
		for _, source := range userInfo.PermissionSources {
			switch {
			case source.Source.Org != "":
				ret["org:"+source.Source.Org] = source.Permission
			case source.Source.Team != "":
				ret["team:"+teamFullnames[source.Source.Team]] = source.Permission
			case source.Source.Repo != "":
				ret["user:"+userInfo.Node.Login] = source.Permission
			}
		}
	}
	return ret, nil
}

type RepoHandle struct {
	Name string
	URL  string
}

func getRepos() ([]RepoHandle, error) {
	query := `					
query($cursor: String) {
  organization(login: "datawire") {
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
	err := graphql(&rawRepos, query, nil)
	if err != nil {
		return nil, err
	}
	var repos []RepoHandle
	for _, repoInfo := range rawRepos.Organization.Repositories.Nodes {
		if repoInfo.IsArchived {
			continue
		}
		repos = append(repos, RepoHandle{Name: repoInfo.Name, URL: repoInfo.URL})
	}
	for rawRepos.Organization.Repositories.PageInfo.HasNextPage {
		err := graphql(&rawRepos, query, map[string]interface{}{
			"cursor": rawRepos.Organization.Repositories.PageInfo.EndCursor,
		})
		if err != nil {
			return nil, err
		}
		for _, repoInfo := range rawRepos.Organization.Repositories.Nodes {
			if repoInfo.IsArchived {
				continue
			}
			repos = append(repos, RepoHandle{Name: repoInfo.Name, URL: repoInfo.URL})
		}
	}
	return repos, nil
}

func Main() error {
	teamFullnames, err := getTeamFullnames()
	if err != nil {
		return err
	}
	repos, err := getRepos()
	if err != nil {
		return err
	}
	for _, repo := range repos {
		collaborators, err := getCollaborators(teamFullnames, repo.Name)
		if err != nil {
			return fmt.Errorf("%s: %w", repo.URL, err)
		}
		strs := make([]string, 0, len(collaborators))
		for k, v := range collaborators {
			strs = append(strs, k+"="+v)
		}
		sort.Strings(strs)
		fmt.Println(repo.URL, strings.Join(strs, " "))
	}

	return nil
}

func main() {
	if err := Main(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
