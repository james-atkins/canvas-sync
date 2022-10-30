package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/peterhellberg/link"
)

type Course struct {
	Id   uint64 `json:"id"`
	Name string `json:"name"`
}

type Folder struct {
	Id           uint64    `json:"id"`
	ParentId     uint64    `json:"parent_folder_id"` // zero if no parent
	Name         string    `json:"name"`
	Path         string    `json:"full_name"`
	UpdatedAt    time.Time `json:"updated_at"`
	FoldersCount uint64    `json:"folders_count"`
	FilesCount   uint64    `json:"files_count"`
}

type File struct {
	Id          uint64    `json:"id"`
	FolderId    uint64    `json:"folder_id"`
	FileName    string    `json:"display_name"`
	Size        int64     `json:"size"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	DownloadUrl string    `json:"url"`
}

type CanvasApi struct {
	Client  *http.Client
	RootUrl string
	Token   string
}

func (api *CanvasApi) MakeCoursesUrl() string {
	return fmt.Sprintf("%s/api/v1/courses?per_page=100", api.RootUrl)
}

func (canvas *CanvasApi) Courses(ctx context.Context, url string) (courses []Course, next string, err error) {
	courses, next, err = callAPI[Course](canvas, canvas.Client, url)
	return
}

func (api *CanvasApi) MakeFoldersInCourseUrl(courseId uint64) string {
	return fmt.Sprintf("%s/api/v1/courses/%d/folders?per_page=100", api.RootUrl, courseId)
}

func (canvas *CanvasApi) FoldersInCourse(ctx context.Context, url string) (folders []Folder, next string, err error) {
	folders, next, err = callAPI[Folder](canvas, canvas.Client, url)
	return
}

func (api *CanvasApi) MakeFilesInFolderUrl(folderId uint64) string {
	return fmt.Sprintf("%s/api/v1/folders/%d/files?per_page=100", api.RootUrl, folderId)
}

func (canvas *CanvasApi) FilesInFolder(ctx context.Context, url string) (files []File, next string, err error) {
	files, next, err = callAPI[File](canvas, canvas.Client, url)
	return
}

func (canvas *CanvasApi) DownloadFile(ctx context.Context, w io.WriteCloser, downloadUrl string) error {
	req, err := http.NewRequestWithContext(ctx, "GET", downloadUrl, nil)
	if err != nil {
		return err
	}

	resp, err := canvas.Client.Do(req)
	if err != nil {
		return fmt.Errorf("client error for %s: %w", downloadUrl, err)
	}

	// TODO: rate limiting

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP error for %s: %d", downloadUrl, resp.StatusCode)
	}

	defer resp.Body.Close()
	_, err = io.Copy(w, resp.Body)
	if err != nil {
		return err
	}

	return w.Close()
}

var errForbidden error = errors.New("forbidden")

func callAPI[T interface{}](canvas *CanvasApi, client *http.Client, apiCall string) ([]T, string, error) {
	req, err := http.NewRequestWithContext(context.TODO(), "GET", apiCall, nil)
	if err != nil {
		return nil, "", fmt.Errorf("new request error for %s: %w", apiCall, err)
	}

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", canvas.Token))

	res, err := client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("client error for %s: %w", apiCall, err)
	}

	// TODO: rate limiting
	// X-Rate-Limit-Remaining
	// X-Request-Cost
	// res.StatusCode == http.StatusTooManyRequests

	if res.StatusCode == http.StatusForbidden {
		return nil, "", errForbidden
	}

	if res.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("HTTP error for %s: %d", apiCall, res.StatusCode)
	}

	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, "", fmt.Errorf("HTTP read error for %s: %w", apiCall, err)
	}

	// Check Link header for next paginated request
	var next string
	for _, l := range link.ParseResponse(res) {
		if l.Rel == "next" {
			next = l.URI
			break
		}
	}

	var j []T
	if err := json.Unmarshal(body, &j); err != nil {
		return nil, "", fmt.Errorf("JSON error for %s: %w", apiCall, err)
	}

	return j, next, nil
}
