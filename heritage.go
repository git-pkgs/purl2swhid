package purl2swhid

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	swh "github.com/andrew/swh-go"
)

type SaveRequest struct {
	ID     int    `json:"id"`
	Status string `json:"status"`
	Task   string `json:"task,omitempty"`
}

func checkArchive(ctx context.Context, result *Result, opts Options) error {
	var options []swh.Option
	if opts.SWHURL != "" {
		options = append(options, swh.WithBaseURL(opts.SWHURL))
	} else {
		options = append(options, swh.WithOfflineToken(opts.Token))
	}
	if opts.HTTPClient != nil {
		options = append(options, swh.WithHTTPClient(opts.HTTPClient))
	}
	client, err := swh.New(options...)
	if err != nil {
		return err
	}
	response, err := client.API.Api1ResolveSwhidWithResponse(ctx, result.SWHID, nil, acceptJSON)
	if err != nil {
		return fmt.Errorf("check Software Heritage: %w", err)
	}
	known := false
	switch response.StatusCode() {
	case http.StatusOK:
		if response.JSON200 == nil || response.JSON200.ObjectId == nil ||
			*response.JSON200.ObjectId != result.SWHID[strings.LastIndexByte(result.SWHID, ':')+1:] {
			return errors.New("invalid resolve response from Software Heritage")
		}
		known = true
	case http.StatusNotFound:
	default:
		return fmt.Errorf("check Software Heritage: HTTP %d", response.StatusCode())
	}
	result.Known = &known
	if opts.Save && !known {
		return saveArchive(ctx, client, result)
	}
	return nil
}

func saveArchive(ctx context.Context, client *swh.Client, result *Result) error {
	origin, err := url.Parse(result.ArchiveURL)
	if err != nil || origin.Scheme != "https" || origin.Host == "" || origin.User != nil {
		return errors.New("saving requires an HTTPS archive URL without credentials")
	}
	response, err := client.API.Api1SaveOriginPostWithResponse(
		ctx, nil, acceptJSON, func(_ context.Context, request *http.Request) error {
			query := request.URL.Query()
			query.Set("visit_type", "tarball")
			query.Set("origin_url", result.ArchiveURL)
			request.URL.RawQuery = query.Encode()
			return nil
		},
	)
	if err != nil {
		return fmt.Errorf("save to Software Heritage: %w", err)
	}
	if response.StatusCode() != http.StatusOK || response.JSON200 == nil {
		return fmt.Errorf("save to Software Heritage: invalid response (HTTP %d)", response.StatusCode())
	}
	saved := response.JSON200
	if saved.Id == nil || *saved.Id <= 0 || saved.SaveRequestStatus == nil || saved.OriginUrl == nil ||
		*saved.OriginUrl != result.ArchiveURL || saved.VisitType == nil || *saved.VisitType != "tarball" {
		return errors.New("invalid save response from Software Heritage")
	}
	request := &SaveRequest{ID: *saved.Id, Status: *saved.SaveRequestStatus}
	if saved.SaveTaskStatus != nil {
		request.Task = *saved.SaveTaskStatus
	}
	switch request.Status {
	case "accepted", "pending":
		result.Save = request
		return nil
	case "rejected":
		return fmt.Errorf("save request %d rejected by Software Heritage", request.ID)
	default:
		return fmt.Errorf("unknown save status %q from Software Heritage", request.Status)
	}
}

func acceptJSON(_ context.Context, request *http.Request) error {
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "purl2swhid")
	return nil
}
