package profile

import "slices"

func cloneMCPPointer[T any](value *T) *T {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneMCPCatalog(c MCPCatalog) MCPCatalog {
	c.RefreshedAt = cloneMCPPointer(c.RefreshedAt)
	c.Tools = append([]MCPTool{}, c.Tools...)
	for i := range c.Tools {
		c.Tools[i].InputSchema = slices.Clone(c.Tools[i].InputSchema)
		c.Tools[i].OutputSchema = slices.Clone(c.Tools[i].OutputSchema)
	}
	return c
}

func cloneMCPServer(s MCPServer) MCPServer {
	s.Catalog = cloneMCPCatalog(s.Catalog)
	if s.Stdio != nil {
		s.Stdio = cloneMCPPointer(s.Stdio)
		s.Stdio.Argv = slices.Clone(s.Stdio.Argv)
		s.Stdio.Env = slices.Clone(s.Stdio.Env)
		for i := range s.Stdio.Env {
			s.Stdio.Env[i].Value.Literal = cloneMCPPointer(s.Stdio.Env[i].Value.Literal)
		}
	}
	if s.HTTP != nil {
		s.HTTP = cloneMCPPointer(s.HTTP)
		s.HTTP.Headers = slices.Clone(s.HTTP.Headers)
		for i := range s.HTTP.Headers {
			s.HTTP.Headers[i].Value.Literal = cloneMCPPointer(s.HTTP.Headers[i].Value.Literal)
		}
		s.HTTP.Bearer = cloneMCPPointer(s.HTTP.Bearer)
		if s.HTTP.OAuth != nil {
			s.HTTP.OAuth = cloneMCPPointer(s.HTTP.OAuth)
			o := s.HTTP.OAuth
			o.ClientSecret = cloneMCPPointer(o.ClientSecret)
			o.SessionRef = cloneMCPPointer(o.SessionRef)
			o.Scopes = slices.Clone(o.Scopes)
			o.GrantedScopes = slices.Clone(o.GrantedScopes)
			o.AccessTokenExpires = cloneMCPPointer(o.AccessTokenExpires)
		}
	}
	return s
}
