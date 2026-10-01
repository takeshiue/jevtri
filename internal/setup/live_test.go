package setup

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/takeshiue/jevtri/internal/jev"
)

// TestLiveFormatDetection sends real requests to Jev. It runs only when
// JEVTRI_LIVE_ENV names a .env file holding JEV_API_KEY, so ordinary test
// runs and CI never send anything. Only testdata and invented lines are
// sent. Record results in docs/experiments.
func TestLiveFormatDetection(t *testing.T) {
	envFile := os.Getenv("JEVTRI_LIVE_ENV")
	if envFile == "" {
		t.Skip("set JEVTRI_LIVE_ENV to a .env file with JEV_API_KEY to send to Jev")
	}
	key := readKey(t, envFile)
	client := jev.NewClient(key)

	root := fakeRoot(t, ubuntu, map[string]string{
		"/s/nginx-error":  "nginx/error.log",
		"/s/httpd-access": "httpd/access_log",
		"/s/httpd-error":  "httpd/error_log",
		"/s/mysql":        "mysql/error.log",
		"/s/mariadb":      "mariadb/error.log",
		"/s/postgres":     "postgres/postgresql.log",
		"/s/redis":        "redis/redis.log",
		"/s/php-fpm":      "php-fpm/php-fpm.log",
		"/s/tomcat":       "tomcat/catalina.2026-09-28.log",
		"/s/mongodb":      "mongodb/mongod.log",
		"/s/messages":     "alma9/messages",
		"/s/audit":        "alma8-host/audit.log",
		"/s/dpkg":         "ubuntu2404/dpkg.log",
		"/s/log4j":        "=2026-09-28 15:47:01,123 INFO  [main] com.example.shop.App - Starting application\n2026-09-28 15:47:02,456 WARN  [pool-1] com.example.shop.Db - Slow query took 2300 ms\n2026-09-28 15:47:03,789 ERROR [pool-1] com.example.shop.Db - Connection refused\n",
		"/s/us-app":       "=09/28/2026 15:47:01 INFO order service started\n09/28/2026 15:47:05 ERROR payment gateway timeout\n",
		"/s/eu-app":       "=28/09/2026 15:47:01 INFO order service started\n28/09/2026 15:47:05 ERROR payment gateway timeout\n",
		"/s/boot":         "=[  OK  ] Started Show Plymouth Boot Screen.\n[  OK  ] Reached target Paths.\n[FAILED] Failed to start nginx.service.\n",
		"/s/dotted":       "=2026.09.28-03:02:05 app: started\n2026.09.28-03:02:09 app: queue full\n",
	})
	want := map[string]string{
		"/s/nginx-error": "slash-ymd", "/s/httpd-access": "apache-access", "/s/httpd-error": "apache-error",
		"/s/mysql": "rfc3339", "/s/mariadb": "iso-space", "/s/postgres": "iso-space", "/s/redis": "dmy-month",
		"/s/php-fpm": "dmy-month", "/s/tomcat": "dmy-month", "/s/mongodb": "rfc3339", "/s/messages": "syslog",
		"/s/audit": "epoch", "/s/dpkg": "iso-space", "/s/log4j": "iso-comma", "/s/us-app": "slash-mdy",
		"/s/eu-app": "slash-dmy", "/s/boot": jev.NoFormat, "/s/dotted": jev.NoFormat,
	}
	now := time.Now()
	correct, registered := 0, 0
	for path, expected := range want {
		lines := headLines(root, path)
		request := jev.BuildFormatRequest(path, strings.Join(lines, "\n"), formatChoices())
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		result, err := client.AskFormat(ctx, request)
		cancel()
		if err != nil {
			t.Errorf("%s: %v", path, err)
			continue
		}
		best := result.Best()
		verified := best.Name != jev.NoFormat && readsLines(best.Name, lines, now)
		if best.Name == expected {
			correct++
		}
		// What init would write: the answer only when it reads the lines.
		written := ""
		if verified {
			written = best.Name
			registered++
		}
		status := "ok"
		switch {
		case written != "" && written != expected:
			status = "WRONG FORMAT WRITTEN"
			t.Errorf("%s: %s would be written, want %s", path, written, expected)
		case best.Name != expected:
			status = "miss"
		}
		t.Logf("%-16s want=%-13s best=%-13s p=%.2f written=%-13q %s %dms", path, expected, best.Name, best.Probability, written, status, result.Latency.Milliseconds())
	}
	t.Logf("correct %d/%d, registered %d", correct, len(want), registered)
}

func readKey(t *testing.T, path string) string {
	t.Helper()
	handle, err := os.Open(filepath.Clean(path))
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	scanner := bufio.NewScanner(handle)
	for scanner.Scan() {
		line := strings.TrimPrefix(strings.TrimSpace(scanner.Text()), "export ")
		if value, ok := strings.CutPrefix(line, "JEV_API_KEY="); ok {
			if value = strings.Trim(value, `"'`); value != "" {
				return value
			}
		}
	}
	t.Fatal("JEV_API_KEY is not in the file")
	return ""
}
