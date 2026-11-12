package good_winrm

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// psQuote quotes s as a PowerShell single-quoted string literal.
func psQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func (c *WinrmConnection) runPS(script string) (string, error) {
	stdout, stderr, _, err := c.client.RunPSWithContext(context.Background(), script)
	if err != nil {
		return "", err
	} else if stderr != "" {
		return "", errors.New(stderr)
	}
	return stdout, nil
}

func (c *WinrmConnection) Upload(localPath, remotePath string, progress io.Writer) error {
	data, err := os.ReadFile(localPath)
	if err != nil {
		return err
	}

	// Create or truncate, so empty files still get created
	_, err = c.runPS(fmt.Sprintf("[System.IO.File]::WriteAllBytes(%s, [byte[]]@())", psQuote(remotePath)))
	if err != nil {
		return err
	}

	// Chunk size (stay well under 8KB command limit)
	chunkSize := 5000

	for i := 0; i < len(data); i += chunkSize {
		end := min(i+chunkSize, len(data))
		encoded := base64.StdEncoding.EncodeToString(data[i:end])

		_, err := c.runPS(fmt.Sprintf(`
            $data = [System.Convert]::FromBase64String('%s')
            Add-Content -Path %s -Value $data -Encoding Byte
        `, encoded, psQuote(remotePath)))
		if err != nil {
			return err
		}
		fmt.Fprintf(progress, "%s: %d%% (%d/%d) bytes uploaded\n", remotePath, end*100/len(data), end, len(data))
	}

	return nil
}

func (c *WinrmConnection) Download(remotePath, localPath string, progress io.Writer) error {
	// Read and encode on remote side
	stdout, err := c.runPS(fmt.Sprintf(`
        $data = [System.IO.File]::ReadAllBytes(%s)
        [System.Convert]::ToBase64String($data)
    `, psQuote(remotePath)))
	if err != nil {
		return err
	}

	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(stdout))
	if err != nil {
		return err
	}

	if err := os.WriteFile(localPath, decoded, 0600); err != nil {
		return err
	}
	fmt.Fprintf(progress, "%s: %d bytes downloaded\n", localPath, len(decoded))
	return nil
}
