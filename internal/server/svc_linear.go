package server

import "github.com/3xDevOps/Aether/internal/linear"

// Members' personal Linear API keys, one 0600 file each under the data
// directory's linear/, never handed to a container.
func init() {
	registerService("linear", func(d Deps) (Service, error) {
		if d.DataDir == "" {
			return nil, nil
		}
		d.SSH.Services.Linear = &linear.Service{Keys: linear.NewKeys(linear.Dir(d.DataDir)), Client: linear.NewClient()}
		return nil, nil
	})
}
