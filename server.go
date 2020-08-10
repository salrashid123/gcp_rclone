// Copyright 2020 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"fmt"
	"log"
	"os"

	_ "github.com/rclone/rclone/backend/all"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/sync"

	"errors"
	"net/http"
	"strings"

	jwt "github.com/dgrijalva/jwt-go"
	"github.com/lestrrat/go-jwx/jwk"
	"golang.org/x/net/http2"

	"github.com/gorilla/mux"
	//secretmanager "cloud.google.com/go/secretmanager/apiv1"
	//secretmanagerpb "google.golang.org/genproto/googleapis/cloud/secretmanager/v1"
)

type gcpIdentityDoc struct {
	// Google struct {
	// 	ComputeEngine struct {
	// 		InstanceCreationTimestamp int64  `json:"instance_creation_timestamp,omitempty"`
	// 		InstanceID                string `json:"instance_id,omitempty"`
	// 		InstanceName              string `json:"instance_name,omitempty"`
	// 		ProjectID                 string `json:"project_id,omitempty"`
	// 		ProjectNumber             int64  `json:"project_number,omitempty"`
	// 		Zone                      string `json:"zone,omitempty"`
	// 	} `json:"compute_engine"`
	// } `json:"google"`
	Email           string `json:"email,omitempty"`
	EmailVerified   bool   `json:"email_verified,omitempty"`
	AuthorizedParty string `json:"azp,omitempty"`
	jwt.StandardClaims
}

type contextKey string

const (
	jwksURL                    = "https://www.googleapis.com/oauth2/v3/certs"
	contextEventKey contextKey = "event"
)

var (
	jwtSet *jwk.Set

	gcsSrc     = os.Getenv("GCS_SRC")
	gcsDest    = os.Getenv("GCS_DEST")
	myAudience = os.Getenv("AUDIENCE")
)

func getKey(token *jwt.Token) (interface{}, error) {
	keyID, ok := token.Header["kid"].(string)
	if !ok {
		return nil, errors.New("expecting JWT header to have string kid")
	}
	if key := jwtSet.LookupKeyID(keyID); len(key) == 1 {
		return key[0].Materialize()
	}
	return nil, errors.New("unable to find key")
}

func verifyGoogleIDToken(ctx context.Context, aud string, rawToken string) (gcpIdentityDoc, error) {
	token, err := jwt.ParseWithClaims(rawToken, &gcpIdentityDoc{}, getKey)
	if err != nil {
		log.Printf("Error parsing JWT %v", err)
		return gcpIdentityDoc{}, err
	}
	if claims, ok := token.Claims.(*gcpIdentityDoc); ok && token.Valid {
		log.Printf("OIDC doc has Audience [%s]   Issuer [%v]", claims.Audience, claims.StandardClaims.Issuer)
		return *claims, nil
	}
	return gcpIdentityDoc{}, errors.New("Error parsing JWT Claims")
}

func authMiddleware(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Println("/authMiddleware called")

		authHeader := r.Header.Get("Authorization")

		if authHeader == "" {
			http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
			return
		}
		splitToken := strings.Split(authHeader, "Bearer")
		if len(splitToken) > 0 {
			tok := strings.TrimSpace(splitToken[1])
			idDoc, err := verifyGoogleIDToken(r.Context(), myAudience, tok)
			if err != nil {
				http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
				return
			}
			// TODO: optionally validate the inbound service account for Cloud Scheduler here.
			log.Printf("Authenticated email: %v", idDoc.Email)
			// Emit the id token into the request Context
			ctx := context.WithValue(r.Context(), contextEventKey, idDoc)
			h.ServeHTTP(w, r.WithContext(ctx))
			return
		}
		http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
		return
	})
}

func defaulthandler(w http.ResponseWriter, r *http.Request) {

	// recall the callingserviceAccount Email address
	// val := r.Context().Value(contextEventKey).(gcpIdentityDoc)

	// TODO: emits stats to stdout
	// fs.Config.StatsLogLevel.Set("DEBUG")

	fsrc, err := fs.NewFs(fmt.Sprintf("gcs-src:%s", gcsSrc))
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	fdest, err := fs.NewFs(fmt.Sprintf("gcs-src:%s", gcsDest))
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	err = sync.Sync(r.Context(), fdest, fsrc, false)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	fmt.Fprint(w, "ok")
}

func main() {

	var err error
	jwtSet, err = jwk.FetchHTTP(jwksURL)
	if err != nil {
		log.Fatal("Unable to load JWK Set: ", err)
	}

	if myAudience == "" || gcsSrc == "" || gcsDest == "" {
		log.Fatalln("Audience, gcsSRC, gcsDest values must be set")
	}

	// Configure the source and destination
	fs.ConfigFileSet("gcs-src", "type", "google cloud storage")
	fs.ConfigFileSet("gcs-src", "bucket_policy_only", "true")

	// For GCP Secret Manager
	//
	// Get access_key
	//
	// access_key_name := fmt.Sprintf("projects/%s/secrets/%s/versions/latest", PROJECT_ID, "access_key_id")
	// access_key_req := &secretmanagerpb.AccessSecretVersionRequest{
	// 	Name: access_key_name,
	// }

	// access_key_result, err := client.AccessSecretVersion(ctx, access_key_req)
	// if err != nil {
	// 	log.Fatalf("failed to access secret version: %v", err)
	// }
	// access_key := access_key_result.Payload.Data

	// Get secret_access_key
	//
	// secret_access_key_name := fmt.Sprintf("projects/%s/secrets/%s/versions/latest", PROJECT_ID, "secret_access_key")
	// secret_access_key_req := &secretmanagerpb.AccessSecretVersionRequest{
	// 	Name: secret_access_key_req,
	// }

	// secret_access_key_result, err := client.AccessSecretVersion(ctx, secret_access_key_req)
	// if err != nil {
	// 	log.Fatalf("failed to access secret version: %v", err)
	// }
	// access_key := secret_access_key_result.Payload.Data

	router := mux.NewRouter()
	router.Methods(http.MethodGet).Path("/").HandlerFunc(defaulthandler)

	var server *http.Server
	server = &http.Server{
		Addr:    ":8080",
		Handler: authMiddleware(router),
	}
	http2.ConfigureServer(server, &http2.Server{})

	err = server.ListenAndServe()
	if err != nil {
		log.Fatal("ListenAndServe: ", err)
	}
}
