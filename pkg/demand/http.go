package demand

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

const maxResponseBytes = 4 << 20

func tokenFromFile(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	value, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read token file: %w", err)
	}
	token := strings.TrimSpace(string(value))
	if token == "" {
		return "", errors.New("token file is empty")
	}
	return token, nil
}

func readJSONResponse(resp *http.Response) ([]byte, error) {
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxResponseBytes {
		return nil, fmt.Errorf("response exceeds %d bytes", maxResponseBytes)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("unexpected HTTP status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, nil
}
