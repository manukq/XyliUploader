package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/gridfs"
	"go.mongodb.org/mongo-driver/mongo/options"
)

type Config struct {
	MongoDB struct {
		URI      string `json:"uri"`
		Database string `json:"database"`
	} `json:"mongodb"`
	Server struct {
		Port int    `json:"port"`
		Host string `json:"host"`
	} `json:"server"`
	Upload struct {
		MaxSize int64  `json:"maxSize"`
		BaseURL string `json:"baseURL"`
	} `json:"upload"`
	OAuth struct {
		Discord struct {
			ClientID     string `json:"clientID"`
			ClientSecret string `json:"clientSecret"`
		} `json:"discord"`
		Twitch struct {
			ClientID     string `json:"clientID"`
			ClientSecret string `json:"clientSecret"`
		} `json:"twitch"`
		GitHub struct {
			ClientID     string `json:"clientID"`
			ClientSecret string `json:"clientSecret"`
		} `json:"github"`
	} `json:"oauth"`
}

var (
	client    *mongo.Client
	gfsBucket *gridfs.Bucket
	config    Config
)

// =-=-=-=-=-=-=-=-XYLIUPLOADER-=-=-=-=-=-=-=-=

// Привет. Это Манук. Пару слов о проекте: Здесь используется база данных MongoDB для хранения файлов в GridFS.
// Каждый файл получает уникальный короткий идентификатор для доступа и отдельный токен для удаления.
// Веб-сервер обрабатывает загрузку, просмотр и удаление файлов через HTTP эндпоинты.
// В example.config.json указаны основные настройки, такие как подключение к базе данных и ограничения на загрузку файлов.
// Если разберётесь - красавы) Удачи!

// =-=-=-=-=-=-=-=-BY=MANUKQ-=-=-=-=-=-=-=-=-=

func init() {
	godotenv.Load()

	configFile, err := os.ReadFile("config.json")
	if err != nil {
		log.Fatal("Error reading config.json:", err)
	}

	err = json.Unmarshal(configFile, &config)
	if err != nil {
		log.Fatal("Error parsing config.json:", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client, err = mongo.Connect(ctx, options.Client().ApplyURI(config.MongoDB.URI))
	if err != nil {
		log.Fatal("Error connecting to MongoDB:", err)
	}

	db := client.Database(config.MongoDB.Database)
	gfsBucket, err = gridfs.NewBucket(db)
	if err != nil {
		log.Fatal("Error creating GridFS bucket:", err)
	}

	log.Printf("Connected to MongoDB at %s", config.MongoDB.URI)
	log.Printf("Using database: %s", config.MongoDB.Database)
}

func generateID() string {
	b := make([]byte, 4)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)[:5]
}

func getStorageUsed(ownerID string) (int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cursor, err := gfsBucket.Find(bson.M{"metadata.owner_id": ownerID})
	if err != nil {
		return 0, err
	}
	defer cursor.Close(ctx)

	var total int64
	for cursor.Next(ctx) {
		var doc struct {
			Length int64 `bson:"length"`
		}
		if err := cursor.Decode(&doc); err != nil {
			return 0, err
		}
		total += doc.Length
	}

	return total, nil
}

func createAlbum(fileIDs []string, ownerID string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	albumID := generateID() + generateID()

	_, err := client.Database(config.MongoDB.Database).Collection("albums").InsertOne(ctx, bson.M{
		"_id":      albumID,
		"files":    fileIDs,
		"owner_id": ownerID,
		"created":  time.Now(),
	})
	if err != nil {
		return "", err
	}

	return albumID, nil
}

func getAlbum(albumID string) (bson.M, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var album bson.M
	err := client.Database(config.MongoDB.Database).Collection("albums").FindOne(ctx, bson.M{"_id": albumID}).Decode(&album)
	if err != nil {
		return nil, err
	}

	return album, nil
}

func getFileType(contentType string) string {
	if strings.HasPrefix(contentType, "image/") {
		return "image"
	}
	if strings.HasPrefix(contentType, "video/") {
		return "video"
	}
	if strings.HasPrefix(contentType, "audio/") {
		return "audio"
	}
	return "file"
}

func formatSize(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

func jsonError(w http.ResponseWriter, message string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}

func main() {
	defer client.Disconnect(context.Background())

	http.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))

	http.HandleFunc("/favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "static/favicon.ico")
	})

	http.HandleFunc("/auth/login/", handleOAuthLogin)
	http.HandleFunc("/auth/callback/", handleOAuthCallback)
	http.HandleFunc("/auth/logout", handleLogout)
	http.HandleFunc("/auth/me", func(w http.ResponseWriter, r *http.Request) {
		user, ok := getSessionUser(r)
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]string{"error": "Not authenticated"})
			return
		}
		json.NewEncoder(w).Encode(map[string]string{
			"username": user["username"].(string),
			"provider": user["provider"].(string),
		})
	})
	http.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		tmpl := template.Must(template.ParseFiles("templates/login.html"))
		tmpl.Execute(w, nil)
	})

	http.HandleFunc("/album/create", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			jsonError(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		if !requireCSRF(w, r) {
			return
		}

		var req struct {
			FileIDs []string `json:"file_ids"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, "Bad request", http.StatusBadRequest)
			return
		}

		if len(req.FileIDs) == 0 {
			jsonError(w, "No files", http.StatusBadRequest)
			return
		}

		var ownerID string
		if user, ok := getSessionUser(r); ok {
			ownerID = user["_id"].(string)
		}

		albumID, err := createAlbum(req.FileIDs, ownerID)
		if err != nil {
			jsonError(w, "Album creation error", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"album_id": albumID,
			"link":     fmt.Sprintf("%s/album/%s", config.Upload.BaseURL, albumID),
		})
	})

	http.HandleFunc("/album/", func(w http.ResponseWriter, r *http.Request) {
		albumID := strings.TrimPrefix(r.URL.Path, "/album/")
		if albumID == "" {
			http.NotFound(w, r)
			return
		}

		album, err := getAlbum(albumID)
		if err != nil {
			http.NotFound(w, r)
			return
		}

		fileIDs := album["files"].([]interface{})

		type albumFile struct {
			ID       string `json:"id"`
			Filename string `json:"filename"`
			Size     string `json:"size"`
			Type     string `json:"type"`
		}

		var files []albumFile
		for _, fid := range fileIDs {
			fileID := fid.(string)

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			cursor, err := gfsBucket.Find(bson.M{"metadata.short_id": fileID})
			if err != nil {
				cancel()
				continue
			}

			var fileDoc struct {
				Filename string `bson:"filename"`
				Length   int64  `bson:"length"`
				Metadata struct {
					ContentType string `bson:"content_type"`
				} `bson:"metadata"`
			}

			if cursor.Next(ctx) {
				cursor.Decode(&fileDoc)
				files = append(files, albumFile{
					ID:       fileID,
					Filename: fileDoc.Filename,
					Size:     formatSize(fileDoc.Length),
					Type:     getFileType(fileDoc.Metadata.ContentType),
				})
			}
			cursor.Close(ctx)
			cancel()
		}

		data := struct {
			AlbumID string      `json:"album_id"`
			Files   []albumFile `json:"files"`
		}{
			AlbumID: albumID,
			Files:   files,
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(data)
	})

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			if r.Method != http.MethodGet {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			setCSRFCookie(w)
			tmpl := template.Must(template.ParseFiles("templates/index.html"))
			err := tmpl.Execute(w, nil)
			if err != nil {
				http.Error(w, "template error", http.StatusInternalServerError)
			}
			return
		}

		fileID := r.URL.Path[1:]
		if fileID == "" {
			http.NotFound(w, r)
			return
		}

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		cursor, err := gfsBucket.Find(bson.M{"metadata.short_id": fileID})
		if err != nil {
			http.Error(w, "file not found", http.StatusNotFound)
			return
		}
		defer cursor.Close(ctx)

		var fileDoc struct {
			Filename string `bson:"filename"`
			Length   int64  `bson:"length"`
			Metadata struct {
				ContentType string `bson:"content_type"`
			} `bson:"metadata"`
		}

		if !cursor.Next(ctx) {
			http.Error(w, "file not found", http.StatusNotFound)
			return
		}

		err = cursor.Decode(&fileDoc)
		if err != nil {
			http.Error(w, "decode error", http.StatusInternalServerError)
			return
		}

		fileType := getFileType(fileDoc.Metadata.ContentType)

		data := struct {
			FileID   string
			Filename string
			FileSize string
		}{
			FileID:   fileID,
			Filename: fileDoc.Filename,
			FileSize: formatSize(fileDoc.Length),
		}

		var tmpl *template.Template
		switch fileType {
		case "image":
			tmpl = template.Must(template.ParseFiles("templates/viewer_image.html"))
		case "video":
			tmpl = template.Must(template.ParseFiles("templates/viewer_video.html"))
		case "audio":
			tmpl = template.Must(template.ParseFiles("templates/viewer_audio.html"))
		default:
			tmpl = template.Must(template.ParseFiles("templates/viewer_file.html"))
		}

		tmpl.Execute(w, data)
	})

	http.HandleFunc("/integrations", func(w http.ResponseWriter, r *http.Request) {
		tmpl := template.Must(template.ParseFiles("templates/integrations.html"))
		tmpl.Execute(w, nil)
	})

	http.HandleFunc("/deployment", func(w http.ResponseWriter, r *http.Request) {
		tmpl := template.Must(template.ParseFiles("templates/deployment.html"))
		tmpl.Execute(w, nil)
	})

	http.HandleFunc("/raw/", func(w http.ResponseWriter, r *http.Request) {
		fileID := r.URL.Path[len("/raw/"):]
		if fileID == "" {
			http.Error(w, "no file id", http.StatusBadRequest)
			return
		}

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		cursor, err := gfsBucket.Find(bson.M{"metadata.short_id": fileID})
		if err != nil {
			http.Error(w, "file not found", http.StatusNotFound)
			return
		}
		defer cursor.Close(ctx)

		var fileDoc struct {
			ID       interface{} `bson:"_id"`
			Filename string      `bson:"filename"`
			Metadata struct {
				ContentType string `bson:"content_type"`
			} `bson:"metadata"`
		}

		if !cursor.Next(ctx) {
			http.Error(w, "file not found", http.StatusNotFound)
			return
		}

		err = cursor.Decode(&fileDoc)
		if err != nil {
			http.Error(w, "decode error", http.StatusInternalServerError)
			return
		}

		downloadStream, err := gfsBucket.OpenDownloadStream(fileDoc.ID)
		if err != nil {
			http.Error(w, "download error", http.StatusInternalServerError)
			return
		}
		defer downloadStream.Close()

		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")

		ct := fileDoc.Metadata.ContentType
		if ct == "text/html" || ct == "image/svg+xml" {
			ct = "application/octet-stream"
		}

		w.Header().Set("Content-Type", ct)
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", fileDoc.Filename))
		io.Copy(w, downloadStream)
	})

	http.HandleFunc("/upload", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			jsonError(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		if !rateLimit(uploadLimiter, w, r) {
			return
		}

		if !requireCSRF(w, r) {
			return
		}

		var maxFileSize int64 = 300 * 1024 * 1024
		var maxFiles = 20
		var maxTotalSize int64 = 1024 * 1024 * 1024
		if user, ok := getSessionUser(r); ok {
			maxFileSize = 1024 * 1024 * 1024
			maxFiles = 100
			maxTotalSize = 5 * 1024 * 1024 * 1024
			_ = user
		}

		err := r.ParseMultipartForm(maxTotalSize)
		if err != nil {
			jsonError(w, "Bad request", http.StatusBadRequest)
			return
		}

		files := r.MultipartForm.File["file"]
		if len(files) == 0 {
			jsonError(w, "No files", http.StatusBadRequest)
			return
		}

		if len(files) > maxFiles {
			jsonError(w, fmt.Sprintf("Too many files (max %d)", maxFiles), http.StatusBadRequest)
			return
		}

		var totalSize int64
		for _, f := range files {
			totalSize += f.Size
		}
		if totalSize > maxTotalSize {
			jsonError(w, fmt.Sprintf("Total size too large (max %d MB)", maxTotalSize/(1024*1024)), http.StatusBadRequest)
			return
		}

		var ownerID string
		if user, ok := getSessionUser(r); ok {
			ownerID = user["_id"].(string)

			usedQuota, err := getStorageUsed(ownerID)
			if err != nil {
				jsonError(w, "Quota check error", http.StatusInternalServerError)
				return
			}

			if usedQuota+totalSize > 20*1024*1024*1024 {
				jsonError(w, "Storage quota exceeded", http.StatusBadRequest)
				return
			}
		}

		type uploadResult struct {
			Filename     string `json:"filename"`
			Link         string `json:"link"`
			DeletionLink string `json:"deletion_link"`
			Error        string `json:"error,omitempty"`
		}

		var results []uploadResult
		var uploadedFileIDs []string

		for _, header := range files {
			file, err := header.Open()
			if err != nil {
				results = append(results, uploadResult{Filename: header.Filename, Error: "Open error"})
				continue
			}

			if header.Size > maxFileSize {
				file.Close()
				results = append(results, uploadResult{Filename: header.Filename, Error: "File too large"})
				continue
			}

			if isBlockedExtension(header.Filename) {
				file.Close()
				results = append(results, uploadResult{Filename: header.Filename, Error: "File type not allowed"})
				continue
			}

			head := make([]byte, 512)
			n, _ := file.Read(head)
			detectedCT := http.DetectContentType(head[:n])

			if isExecutableType(detectedCT) {
				file.Close()
				results = append(results, uploadResult{Filename: header.Filename, Error: "Executable files are not allowed"})
				continue
			}

			detectedCT = safeContentType(detectedCT, header.Filename)

			shortID := generateID()
			deleteToken := generateID() + generateID()

			opts := options.GridFSUpload().SetMetadata(bson.M{
				"short_id":     shortID,
				"delete_token": deleteToken,
				"content_type": detectedCT,
				"owner_id":     ownerID,
			})

			uploadStream, err := gfsBucket.OpenUploadStream(header.Filename, opts)
			if err != nil {
				file.Close()
				results = append(results, uploadResult{Filename: header.Filename, Error: "Upload error"})
				continue
			}

			fileReader := io.MultiReader(bytes.NewReader(head[:n]), file)
			_, err = io.Copy(uploadStream, fileReader)
			uploadStream.Close()
			file.Close()

			if err != nil {
				results = append(results, uploadResult{Filename: header.Filename, Error: "Write error"})
				continue
			}

			results = append(results, uploadResult{
				Filename:     header.Filename,
				Link:         fmt.Sprintf("%s/%s", config.Upload.BaseURL, shortID),
				DeletionLink: fmt.Sprintf("%s/delete/%s", config.Upload.BaseURL, deleteToken),
			})
			uploadedFileIDs = append(uploadedFileIDs, shortID)
		}

		var albumLink string
		if len(uploadedFileIDs) > 1 {
			albumID, err := createAlbum(uploadedFileIDs, ownerID)
			if err == nil {
				albumLink = fmt.Sprintf("%s/album/%s", config.Upload.BaseURL, albumID)
			}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"files":      results,
			"album_link": albumLink,
		})
	})

	http.HandleFunc("/delete/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			jsonError(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		if !rateLimit(deleteLimiter, w, r) {
			return
		}

		if !requireCSRF(w, r) {
			return
		}

		deleteToken := r.URL.Path[len("/delete/"):]
		if deleteToken == "" {
			jsonError(w, "No delete token", http.StatusBadRequest)
			return
		}

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		cursor, err := gfsBucket.Find(bson.M{"metadata.delete_token": deleteToken})
		if err != nil {
			jsonError(w, "File not found", http.StatusNotFound)
			return
		}
		defer cursor.Close(ctx)

		var fileDoc struct {
			ID       interface{} `bson:"_id"`
			Metadata struct {
				OwnerID string `bson:"owner_id"`
			} `bson:"metadata"`
		}

		if !cursor.Next(ctx) {
			jsonError(w, "File not found", http.StatusNotFound)
			return
		}

		err = cursor.Decode(&fileDoc)
		if err != nil {
			jsonError(w, "Decode error", http.StatusInternalServerError)
			return
		}

		if fileDoc.Metadata.OwnerID != "" {
			user, ok := getSessionUser(r)
			if !ok || user["_id"].(string) != fileDoc.Metadata.OwnerID {
				jsonError(w, "Not authorized to delete this file", http.StatusForbidden)
				return
			}
		}

		err = gfsBucket.Delete(fileDoc.ID)
		if err != nil {
			jsonError(w, "Delete error", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})
	})

	addr := fmt.Sprintf("%s:%d", config.Server.Host, config.Server.Port)
	log.Printf("Starting server on %s", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}
