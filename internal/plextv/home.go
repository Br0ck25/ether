package plextv

import (
	"context"
	"fmt"
	"github.com/braedonsaunders/ether/internal/core"
	"net/http"
	"net/url"
	"strconv"
)

// HomeUserToken uses the owner's authorized Home switch operation. A PIN, when
// configured by Plex, is still required; this never resets or bypasses it.
func (c *Client) HomeUserToken(ctx context.Context, ownerToken string, id int64, pin string) (string, error) {
	if id <= 0 {
		return "", core.ErrUnauthorized
	}
	q := url.Values{}
	if pin != "" {
		q.Set("pin", pin)
	}
	res, err := c.do(ctx, request{method: http.MethodPost, path: "/api/home/users/" + strconv.FormatInt(id, 10) + "/switch", token: ownerToken, query: q, accept: "application/xml"})
	if err != nil {
		return "", err
	}
	root, err := parseXMLNode(res.body)
	if err != nil {
		return "", err
	}
	token := root.attr("authenticationToken", "authToken")
	if token == "" {
		return "", fmt.Errorf("Plex Home did not authorize a profile token")
	}
	account, err := c.User(ctx, token)
	if err != nil {
		return "", err
	}
	if account.ID != id {
		return "", core.ErrUnauthorized
	}
	return token, nil
}
