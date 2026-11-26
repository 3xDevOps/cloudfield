package server

import "github.com/3xDevOps/cloudfield/internal/linear/api"

// Members' personal Linear API keys, one 0600 file each under the data
// directory's linear/, never handed to a container.
func init() {
	registerService("linear", func(d Deps) (Service, error) {
		if d.DataDir == "" {
			return nil, nil
		}
		d.SSH.Services.Linear = &api.Service{Keys: api.NewKeys(api.Dir(d.DataDir)), Client: api.NewClient()}
		return nil, nil
	})
}
