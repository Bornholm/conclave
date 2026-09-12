package github

import (
	"encoding/json"
	"fmt"
)

type user struct {
	Login string `json:"login"`
}

type repository struct {
	Name     string `json:"name"`
	FullName string `json:"full_name"`
	Owner    user   `json:"owner"`
	CloneURL string `json:"clone_url"`
	Fork     bool   `json:"fork"`
}

type ref struct {
	Ref  string      `json:"ref"`
	SHA  string      `json:"sha"`
	Repo *repository `json:"repo"`
}

type label struct {
	Name string `json:"name"`
}

type pullRequest struct {
	Number  int64   `json:"number"`
	Title   string  `json:"title"`
	Body    string  `json:"body"`
	State   string  `json:"state"`
	HTMLURL string  `json:"html_url"`
	User    user    `json:"user"`
	Base    ref     `json:"base"`
	Head    ref     `json:"head"`
	Labels  []label `json:"labels"`
	Draft   bool    `json:"draft"`
}

type pullRequestFile struct {
	Filename         string `json:"filename"`
	PreviousFilename string `json:"previous_filename"`
	Status           string `json:"status"`
	Additions        int    `json:"additions"`
	Deletions        int    `json:"deletions"`
}

type issue struct {
	Number      int64  `json:"number"`
	Title       string `json:"title"`
	Body        string `json:"body"`
	HTMLURL     string `json:"html_url"`
	PullRequest *struct {
		URL string `json:"url"`
	} `json:"pull_request"`
}

type comment struct {
	ID        int64  `json:"id"`
	Body      string `json:"body"`
	User      user   `json:"user"`
	CreatedAt string `json:"created_at"`
}

type review struct {
	ID          int64  `json:"id"`
	Body        string `json:"body"`
	User        user   `json:"user"`
	State       string `json:"state"`
	SubmittedAt string `json:"submitted_at"`
}

type reviewComment struct {
	ID        int64  `json:"id"`
	Body      string `json:"body"`
	User      user   `json:"user"`
	CreatedAt string `json:"created_at"`
	Path      string `json:"path"`
	Line      int    `json:"line"`
}

func decode(data []byte, out any) error {
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("unexpected GitHub response: %w", err)
	}
	return nil
}
