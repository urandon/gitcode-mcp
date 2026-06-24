package gitcode

import (
	"fmt"
	"net/url"
	"strings"
)

func getRepoEndpoint(owner, repo string) string {
	return endpointPath("/api/v5/repos/%s/%s", owner, repo)
}

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
	return wikiContentsPathEndpoint(owner, repo, slug)
}

func listWikiPagesEndpoint(owner, repo string) string {
	return wikiContentsRootEndpoint(owner, repo)
}

func wikiContentsRootEndpoint(owner, repo string) string {
	return endpointPath("/api/v5/repos/%s/%s/contents", owner, repo+".wiki")
}

func wikiContentsPathEndpoint(owner, repo, path string) string {
	return endpointPath("/api/v5/repos/%s/%s/contents", owner, repo+".wiki") + "/" + wikiPathSegments(path)
}

func wikiRawPathEndpoint(owner, repo, path string) string {
	return endpointPath("/api/v5/repos/%s/%s/raw", owner, repo+".wiki") + "/" + wikiPathSegments(path)
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

func deleteWikiPageEndpoint(owner, repo, path string) string {
	return wikiContentsPathEndpoint(owner, repo, path)
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

func wikiPathSegments(value string) string {
	parts := strings.Split(normalizeWikiPath(value), "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}
