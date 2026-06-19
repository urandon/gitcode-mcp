package gitcode

import (
	"fmt"
	"net/url"
)

func listIssuesEndpoint(owner, repo string) string {
	return endpointPath("/api/v5/repos/%s/%s/issues", owner, repo)
}

func getIssueEndpoint(owner, repo string, number int) string {
	return endpointPath("/api/v5/repos/%s/%s/issues/%d", owner, repo, number)
}

func listIssueCommentsEndpoint(owner, repo string, number int) string {
	return endpointPath("/api/v5/repos/%s/%s/issues/%d/comments", owner, repo, number)
}

func getWikiPageEndpoint(owner, repo, slug string) string {
	return endpointPath("/api/v5/repos/%s/%s/wiki/%s", owner, repo, slug)
}

func listWikiPagesEndpoint(owner, repo string) string {
	return endpointPath("/api/v5/repos/%s/%s/wiki", owner, repo)
}

func searchIssuesEndpoint() string {
	return "/api/v5/search"
}

func issueAttachmentsEndpoint(owner, repo string, number int) string {
	return endpointPath("/api/v5/repos/%s/%s/issues/%d/attachments", owner, repo, number)
}

func attachmentEndpoint(owner, repo string, number int, attachmentID string) string {
	return endpointPath("/api/v5/repos/%s/%s/issues/%d/attachments/%s", owner, repo, number, attachmentID)
}

func createIssueEndpoint(owner, repo string) string {
	return listIssuesEndpoint(owner, repo)
}

func updateIssueEndpoint(owner, repo string, number int) string {
	return getIssueEndpoint(owner, repo, number)
}

func createIssueCommentEndpoint(owner, repo string, number int) string {
	return listIssueCommentsEndpoint(owner, repo, number)
}

func createWikiPageEndpoint(owner, repo string) string {
	return listWikiPagesEndpoint(owner, repo)
}

func updateWikiPageEndpoint(owner, repo, slug string) string {
	return getWikiPageEndpoint(owner, repo, slug)
}

func addLabelEndpoint(owner, repo string, number int) string {
	return endpointPath("/api/v5/repos/%s/%s/issues/%d/labels", owner, repo, number)
}

func removeLabelEndpoint(owner, repo string, number int, label string) string {
	return endpointPath("/api/v5/repos/%s/%s/issues/%d/labels/%s", owner, repo, number, label)
}

func endpointPath(format string, args ...any) string {
	escaped := make([]any, len(args))
	for i, arg := range args {
		switch v := arg.(type) {
		case string:
			escaped[i] = url.PathEscape(v)
		default:
			escaped[i] = v
		}
	}
	return fmt.Sprintf(format, escaped...)
}
