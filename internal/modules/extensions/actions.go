package extensions

import (
	"context"
	"fmt"
	"strings"
)

// AllowedActions is the closed set of actions an extension token may invoke.
var AllowedActions = map[string]string{
	"deploy.trigger": "deploy:create",
	"resources.list": "resource:read",
	"projects.list":  "resource:read",
}

// AuthorizeAction checks token scopes contain action and maps to an authz permission string.
// scopes is a space-separated list stored on the token.
func AuthorizeAction(scopes, action string) (perm string, err error) {
	perm, ok := AllowedActions[action]
	if !ok {
		return "", fmt.Errorf("unknown action %q", action)
	}
	want := action
	for _, s := range strings.Fields(scopes) {
		if s == want || s == "*" {
			return perm, nil
		}
	}
	return "", fmt.Errorf("token lacks scope %q", action)
}

// InvokeAction is the shared entry for extension action calls. The caller supplies a permission check
// against the same authz model the REST API uses (never a parallel model).
func (s *Service) InvokeAction(ctx context.Context, token, action string, checkPerm func(perm string) error) error {
	_, scopes, err := s.ResolveToken(ctx, token)
	if err != nil {
		return fmt.Errorf("invalid token")
	}
	perm, err := AuthorizeAction(scopes, action)
	if err != nil {
		return err
	}
	if checkPerm != nil {
		if err := checkPerm(perm); err != nil {
			return err
		}
	}
	return nil
}
