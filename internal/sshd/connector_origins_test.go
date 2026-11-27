package sshd

// ConnectorOrigins is a method of its own file so edge_policy_test.go's
// fake stays as the pull request that adds Notices has it.
func (f *fakeEdgeOwner) ConnectorOrigins() []string { return nil }
