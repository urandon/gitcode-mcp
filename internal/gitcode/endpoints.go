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

func listRepositoryIssueCommentsEndpoint(owner, repo string) string {
	return endpointPath("/api/v5/repos/%s/%s/issues/comments", owner, repo)
}

func listPREndpoint(owner, repo string) string {
	return endpointPath("/api/v5/repos/%s/%s/pulls", owner, repo)
}

func getPREndpoint(owner, repo string, number int) string {
	return endpointPath("/api/v5/repos/%s/%s/pulls/%d", owner, repo, number)
}

func listPRCommentsEndpoint(owner, repo string, number int) string {
	return endpointPath("/api/v5/repos/%s/%s/pulls/%d/comments", owner, repo, number)
}

func createPRCommentEndpoint(owner, repo string, number int) string {
	return listPRCommentsEndpoint(owner, repo, number)
}

func listPRDiscussionsEndpoint(owner, repo string, number int) string {
	project := url.PathEscape(owner + "/" + repo)
	return fmt.Sprintf("/api/v4/projects/%s/merge_requests/%d/discussions", project, number)
}

func replyPRReviewCommentEndpoint(owner, repo string, number int, discussionID string) string {
	return endpointPath("/api/v5/repos/%s/%s/pulls/%d/discussions/%s/comments", owner, repo, number, discussionID)
}

func linkPRIssueEndpoint(owner, repo string, number int) string {
	return endpointPath("/api/v5/repos/%s/%s/pulls/%d/issues", owner, repo, number)
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

func updateIssueCommentEndpoint(owner, repo, commentID string) string {
	return endpointPath("/api/v5/repos/%s/%s/issues/comments/%s", owner, repo, commentID)
}

func getIssueCommentEndpoint(owner, repo, commentID string) string {
	return updateIssueCommentEndpoint(owner, repo, commentID)
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

func listMilestonesEndpoint(owner, repo string) string {
	return endpointPath("/api/v5/repos/%s/%s/milestones", owner, repo)
}

func listPushRemoteMirrorsEndpoint(owner, repo string) string {
	return endpointPath("/api/v5/repos/%s/%s/push_remote_mirrors", owner, repo)
}

func triggerPushRemoteMirrorEndpoint(owner, repo, mirrorID string) string {
	return endpointPath("/api/v5/repos/%s/%s/push_remote_mirrors/%s", owner, repo, mirrorID)
}

func getMilestoneEndpoint(owner, repo string, id int) string {
	return endpointPath("/api/v5/repos/%s/%s/milestones/%d", owner, repo, id)
}

func listReleasesEndpoint(owner, repo string) string {
	return endpointPath("/api/v5/repos/%s/%s/releases", owner, repo)
}

func getReleaseEndpoint(owner, repo, tag string) string {
	return endpointPath("/api/v5/repos/%s/%s/releases/tags/%s", owner, repo, tag)
}

func updateReleaseEndpoint(owner, repo, tag string) string {
	return endpointPath("/api/v5/repos/%s/%s/releases/%s", owner, repo, tag)
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
