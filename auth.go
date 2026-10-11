package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson"
)

const sessionCookieName = "session_id"
const sessionCollection = "sessions"
const usersCollection = "users"

type OAuthProvider struct {
	Name         string
	ClientID     string
	ClientSecret string
	AuthURL      string
	TokenURL     string
	UserInfoURL  string
	Scopes       string
}

var oauthProviders = map[string]*OAuthProvider{
	"discord": {
		Name:        "discord",
		AuthURL:     "https://discord.com/oauth2/authorize",
		TokenURL:    "https://discord.com/api/oauth2/token",
		UserInfoURL: "https://discord.com/api/users/@me",
		Scopes:      "identify email",
	},
	"twitch": {
		Name:        "twitch",
		AuthURL:     "https://id.twitch.tv/oauth2/authorize",
		TokenURL:    "https://id.twitch.tv/oauth2/token",
		UserInfoURL: "https://api.twitch.tv/helix/users",
		Scopes:      "user:read:email",
	},
	"github": {
		Name:        "github",
		AuthURL:     "https://github.com/login/oauth/authorize",
		TokenURL:    "https://github.com/login/oauth/access_token",
		UserInfoURL: "https://api.github.com/user",
		Scopes:      "read:user user:email",
	},
}

func getOAuthClientID(provider string) string {
	switch provider {
	case "discord":
		return config.OAuth.Discord.ClientID
	case "twitch":
		return config.OAuth.Twitch.ClientID
	case "github":
		return config.OAuth.GitHub.ClientID
	}
	return ""
}

func getOAuthClientSecret(provider string) string {
	switch provider {
	case "discord":
		return config.OAuth.Discord.ClientSecret
	case "twitch":
		return config.OAuth.Twitch.ClientSecret
	case "github":
		return config.OAuth.GitHub.ClientSecret
	}
	return ""
}

func generateSessionID() string {
	b := make([]byte, 32)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func createSession(w http.ResponseWriter, userID string, provider string) {
	sessionID := generateSessionID()
	expiresAt := time.Now().Add(7 * 24 * time.Hour)

	_, err := client.Database(config.MongoDB.Database).Collection(sessionCollection).InsertOne(context.Background(), bson.M{
		"session_id": sessionID,
		"user_id":    userID,
		"provider":   provider,
		"expires_at": expiresAt,
	})
	if err != nil {
		log.Printf("Session creation error: %v", err)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    sessionID,
		Path:     "/",
		Expires:  expiresAt,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
}

func destroySession(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return
	}

	client.Database(config.MongoDB.Database).Collection(sessionCollection).DeleteOne(context.Background(), bson.M{"session_id": cookie.Value})

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
}

func getSessionUser(r *http.Request) (bson.M, bool) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return nil, false
	}

	var session bson.M
	err = client.Database(config.MongoDB.Database).Collection(sessionCollection).FindOne(context.Background(), bson.M{"session_id": cookie.Value}).Decode(&session)
	if err != nil {
		return nil, false
	}

	expiresAt, ok := session["expires_at"].(time.Time)
	if !ok || time.Now().After(expiresAt) {
		return nil, false
	}

	userID := session["user_id"].(string)

	var user bson.M
	err = client.Database(config.MongoDB.Database).Collection(usersCollection).FindOne(context.Background(), bson.M{"_id": userID}).Decode(&user)
	if err != nil {
		return nil, false
	}

	return user, true
}

func findOrCreateUser(provider string, providerUserID string, username string, email string) string {
	db := client.Database(config.MongoDB.Database).Collection(usersCollection)

	var user bson.M
	err := db.FindOne(context.Background(), bson.M{
		"provider":         provider,
		"provider_user_id": providerUserID,
	}).Decode(&user)

	if err == nil {
		userID := user["_id"].(string)

		db.UpdateOne(context.Background(), bson.M{"_id": userID}, bson.M{
			"$set": bson.M{"username": username, "email": email},
		})

		return userID
	}

	userID := generateID() + generateID()
	_, err = db.InsertOne(context.Background(), bson.M{
		"_id":              userID,
		"provider":         provider,
		"provider_user_id": providerUserID,
		"username":         username,
		"email":            email,
		"created_at":       time.Now(),
	})
	if err != nil {
		log.Printf("User creation error: %v", err)
		return ""
	}

	return userID
}

func handleOAuthLogin(w http.ResponseWriter, r *http.Request) {
	provider := strings.TrimPrefix(r.URL.Path, "/auth/login/")
	p, ok := oauthProviders[provider]
	if !ok {
		http.Error(w, "Unknown provider", http.StatusBadRequest)
		return
	}

	clientID := getOAuthClientID(provider)
	if clientID == "" {
		http.Error(w, "OAuth not configured for this provider", http.StatusServiceUnavailable)
		return
	}

	stateBytes := make([]byte, 16)
	rand.Read(stateBytes)
	state := base64.RawURLEncoding.EncodeToString(stateBytes)

	redirectURI := fmt.Sprintf("%s/auth/callback/%s", config.Upload.BaseURL, provider)

	url := fmt.Sprintf("%s?client_id=%s&redirect_uri=%s&response_type=code&scope=%s&state=%s",
		p.AuthURL,
		clientID,
		redirectURI,
		p.Scopes,
		state,
	)

	http.Redirect(w, r, url, http.StatusTemporaryRedirect)
}

func handleOAuthCallback(w http.ResponseWriter, r *http.Request) {
	provider := strings.TrimPrefix(r.URL.Path, "/auth/callback/")
	p, ok := oauthProviders[provider]
	if !ok {
		http.Error(w, "Unknown provider", http.StatusBadRequest)
		return
	}

	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "Missing code", http.StatusBadRequest)
		return
	}

	clientID := getOAuthClientID(provider)
	clientSecret := getOAuthClientSecret(provider)

	redirectURI := fmt.Sprintf("%s/auth/callback/%s", config.Upload.BaseURL, provider)

	tokenReq, err := http.NewRequest("POST", p.TokenURL, strings.NewReader(fmt.Sprintf(
		"client_id=%s&client_secret=%s&code=%s&grant_type=authorization_code&redirect_uri=%s",
		clientID, clientSecret, code, redirectURI,
	)))
	if err != nil {
		http.Error(w, "Token request error", http.StatusInternalServerError)
		return
	}
	tokenReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	tokenReq.Header.Set("Accept", "application/json")

	tokenResp, err := http.DefaultClient.Do(tokenReq)
	if err != nil {
		http.Error(w, "Token request failed", http.StatusInternalServerError)
		return
	}
	defer tokenResp.Body.Close()

	body, err := io.ReadAll(tokenResp.Body)
	if err != nil {
		http.Error(w, "Token read error", http.StatusInternalServerError)
		return
	}

	var tokenData map[string]interface{}
	if err := json.Unmarshal(body, &tokenData); err != nil {
		http.Error(w, "Token parse error", http.StatusInternalServerError)
		return
	}

	accessToken, ok := tokenData["access_token"].(string)
	if !ok {
		http.Error(w, "No access token", http.StatusInternalServerError)
		return
	}

	userReq, err := http.NewRequest("GET", p.UserInfoURL, nil)
	if err != nil {
		http.Error(w, "User info request error", http.StatusInternalServerError)
		return
	}
	userReq.Header.Set("Authorization", "Bearer "+accessToken)

	userResp, err := http.DefaultClient.Do(userReq)
	if err != nil {
		http.Error(w, "User info request failed", http.StatusInternalServerError)
		return
	}
	defer userResp.Body.Close()

	userBody, err := io.ReadAll(userResp.Body)
	if err != nil {
		http.Error(w, "User info read error", http.StatusInternalServerError)
		return
	}

	var userData map[string]interface{}
	if err := json.Unmarshal(userBody, &userData); err != nil {
		http.Error(w, "User info parse error", http.StatusInternalServerError)
		return
	}

	var providerUserID, username, email string

	switch provider {
	case "discord":
		providerUserID = userData["id"].(string)
		username = userData["username"].(string)
		email = userData["email"].(string)
	case "twitch":
		data := userData["data"].([]interface{})
		if len(data) > 0 {
			user := data[0].(map[string]interface{})
			providerUserID = user["id"].(string)
			username = user["login"].(string)
			email = user["email"].(string)
		}
	case "github":
		providerUserID = fmt.Sprintf("%.0f", userData["id"].(float64))
		username = userData["login"].(string)
		if emailVal, ok := userData["email"].(string); ok {
			email = emailVal
		}
	}

	userID := findOrCreateUser(provider, providerUserID, username, email)
	if userID == "" {
		http.Error(w, "User creation failed", http.StatusInternalServerError)
		return
	}

	createSession(w, userID, provider)
	http.Redirect(w, r, "/", http.StatusTemporaryRedirect)
}

func handleLogout(w http.ResponseWriter, r *http.Request) {
	destroySession(w, r)
	http.Redirect(w, r, "/", http.StatusTemporaryRedirect)
}
