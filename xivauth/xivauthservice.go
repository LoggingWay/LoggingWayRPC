package xivauth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httputil"
	"time"

	"github.com/gojek/heimdall/v7/httpclient"
	"golang.org/x/oauth2"
)

const (
	AuthBaseURL = "https://xivauth.net"
	APIVersion  = "v1"
)
const (
	ScopeUser       = "user"
	ScopeUserEmail  = "user:email"
	ScopeUserSocial = "user:social"
	ScopeUserJWT    = "user:jwt"

	ScopeCharacter       = "character"
	ScopeCharacterAll    = "character:all"
	ScopeCharacterJWT    = "character:jwt"
	ScopeCharacterManage = "character:manage"

	ScopeRefresh       = "refresh"
	ScopeJWTOnBehalfOf = "jwt:obo"
)

type XivAuthService struct {
	client   *httpclient.Client
	oauthcfg *oauth2.Config
}

type User struct {
	ID               string `json:"id"`
	Verified         bool   `json:"verified_characters"`
	Email            string `json:"email,omitempty"`
	SocialIdentities any    `json:"social_identities,omitempty"`
}

type Character struct {
	PersistentKey string `json:"persistent_key"`
	LodestoneID   string `json:"lodestone_id"`
	Name          string `json:"name"`
	HomeWorld     string `json:"home_world"`
	DataCenter    string `json:"data_center"`

	AvatarURL   string `json:"avatar_url"`
	PortraitURL string `json:"portrait_url"`

	CreatedAt  time.Time  `json:"created_at"`
	VerifiedAt *time.Time `json:"verified_at,omitempty"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

type CharactersResponse struct {
	Characters []Character `json:"characters"`
}

func NewService(clientID string, clientSecret string, redirectURL string, scopes []string) (*XivAuthService, error) {
	oauth2cfg := &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURL:  redirectURL,
		Scopes:       scopes,
		Endpoint: oauth2.Endpoint{
			AuthURL:  AuthBaseURL + "/oauth/authorize",
			TokenURL: AuthBaseURL + "/oauth/token",
		},
	}
	timeout := 1000 * time.Millisecond
	return &XivAuthService{
		client:   httpclient.NewClient((httpclient.WithHTTPTimeout(timeout))),
		oauthcfg: oauth2cfg,
	}, nil
}

func (s *XivAuthService) GetToken(code string) (*oauth2.Token, error) {
	token, err := s.oauthcfg.Exchange(context.Background(), code)
	if err != nil {
		return &oauth2.Token{}, err
	}
	return token, nil
}

func (s *XivAuthService) GetUser(token string) (*User, error) {
	url := fmt.Sprintf("%s/api/%s/user", AuthBaseURL, APIVersion)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json") //needed or this will 406 Not Acceptable TODO:probably put somewhere saner
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		println(resp.Status)
		return nil, fmt.Errorf("unexpected status: %s", resp.Status)
	}
	var user User
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return nil, err
	}

	return &user, nil
}

func (s *XivAuthService) GetCharacters(token string) ([]Character, error) {
	url := fmt.Sprintf("%s/api/%s/characters", AuthBaseURL, APIVersion)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		res, err := httputil.DumpResponse(resp, true)
		if err != nil {
			return nil, fmt.Errorf("unexpected dump error?: %s", err)
		}
		println("response:")
		println(string(res))
		return nil, fmt.Errorf("unexpected status: %s", resp.Status)
	}

	var characters []Character
	if err := json.NewDecoder(resp.Body).Decode(&characters); err != nil {
		return nil, err
	}

	return characters, nil
}
