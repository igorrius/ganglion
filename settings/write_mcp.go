package settings

import (
	"maps"
	"os"
	"slices"
	"time"

	"go.yaml.in/yaml/v3"
)

// Keys of the mcp: section the writers touch.
const (
	keyServers     = "servers"
	keyURL         = "url"
	keyAuth        = "auth"
	keyHeader      = "header"
	keyTimeout     = "timeout"
	keyTools       = "tools"
	keyFingerprint = "fingerprint"
)

// MCPFile is the mcp: section of the node's file as it is now: it reads
// the file on every call, so a change is seen at once (R-CFG-6).
type MCPFile struct {
	Path string
}

// MCP reads the file and returns its mcp: section; a missing file has the
// default. The whole file is checked, as at start.
func (f MCPFile) MCP() (MCP, error) {
	file, err := LoadOptional(f.Path, os.ReadFile)
	if err != nil {
		return MCP{}, err
	}
	return file.MCP, nil
}

// SetServer writes s as the line of name under mcp.servers, replacing the
// line that is there. A line the node would not start with is refused and
// the file is left as it was.
func (f MCPFile) SetServer(name string, s MCPServer) error {
	return editDoc(f.Path, func(doc *yaml.Node) error {
		servers, err := mcpServersNode(doc)
		if err != nil {
			return err
		}
		line := serverNode(s)
		if v, _ := field(servers, name); v != nil {
			line.HeadComment, line.LineComment = v.HeadComment, v.LineComment
			*v = *line
			return nil
		}
		servers.Content = append(servers.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: tagStr, Value: name}, line)
		return nil
	})
}

// SetPolicy writes the policy of one tool of server; the server must have
// a line.
func (f MCPFile) SetPolicy(server, tool string, p MCPPolicy) error {
	return editDoc(f.Path, func(doc *yaml.Node) error {
		servers, err := mcpServersNode(doc)
		if err != nil {
			return err
		}
		line, _ := field(servers, server)
		if line == nil || line.Kind != yaml.MappingNode {
			return pathError("mcp.servers."+server, "no such server")
		}
		tools, err := child(line, keyTools, yaml.MappingNode, tagMap)
		if err != nil {
			return err
		}
		tools.Style &^= yaml.FlowStyle
		setScalar(tools, tool, string(p), tagStr)
		return nil
	})
}

// RemoveServer drops the line of name from mcp.servers; a name that is not
// listed is not an error. An emptied servers: is dropped.
func (f MCPFile) RemoveServer(name string) error {
	return editDoc(f.Path, func(doc *yaml.Node) error {
		servers, err := mcpServersNode(doc)
		if err != nil {
			return err
		}
		if _, at := field(servers, name); at >= 0 {
			servers.Content = slices.Delete(servers.Content, at, at+2)
		}
		if len(servers.Content) == 0 {
			mcp, _ := field(doc.Content[0], keyMCP)
			if _, at := field(mcp, keyServers); at >= 0 {
				mcp.Content = slices.Delete(mcp.Content, at, at+2)
			}
			if len(mcp.Content) == 0 {
				_, at := field(doc.Content[0], keyMCP)
				doc.Content[0].Content = slices.Delete(doc.Content[0].Content, at, at+2)
			}
		}
		return nil
	})
}

// mcpServersNode finds mcp.servers, creating the document, the mapping and
// the keys as needed.
func mcpServersNode(doc *yaml.Node) (*yaml.Node, error) {
	root, err := rootOf(doc)
	if err != nil {
		return nil, err
	}
	mcp, err := child(root, keyMCP, yaml.MappingNode, tagMap)
	if err != nil {
		return nil, err
	}
	servers, err := child(mcp, keyServers, yaml.MappingNode, tagMap)
	if err != nil {
		return nil, err
	}
	servers.Style &^= yaml.FlowStyle
	return servers, nil
}

// serverNode is a server's line as a block mapping, its tools sorted; the
// default timeout is left out.
func serverNode(s MCPServer) *yaml.Node {
	str := func(v string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Tag: tagStr, Value: v} }
	n := &yaml.Node{Kind: yaml.MappingNode, Tag: tagMap}
	n.Content = append(n.Content, plain(keyURL), str(s.URL), plain(keyAuth), str(string(s.Auth)))
	if s.Header != "" {
		n.Content = append(n.Content, plain(keyHeader), str(s.Header))
	}
	if t := time.Duration(s.Timeout); t != 0 && t != DefaultMCPTimeout {
		n.Content = append(n.Content, plain(keyTimeout), str(t.String()))
	}
	tools := &yaml.Node{Kind: yaml.MappingNode, Tag: tagMap}
	for _, name := range slices.Sorted(maps.Keys(s.Tools)) {
		tools.Content = append(tools.Content, plain(name), str(string(s.Tools[name])))
	}
	if len(tools.Content) == 0 {
		tools.Style = yaml.FlowStyle
	}
	n.Content = append(n.Content, plain(keyTools), tools, plain(keyFingerprint), str(s.Fingerprint))
	return n
}
