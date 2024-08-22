package main

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"flag"
	"fmt"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
	"log"
	"net/http"
	"os"
	"os/exec"
	goruntime "runtime"
	"strings"
	"sync"
	"time"
)

const Version = "v2.1.1"

type Result struct {
	URL           string
	OriginalURL   string
	Title         string
	StatusCode    int
	Screenshot    string
	Accessible    bool
	WasRedirected bool
}

func main() {
	fmt.Printf("URL Checker version %s\n", Version)

	// 定义命令行标志
	urlFile := flag.String("file", "", "Path to the file containing URLs")
	concurrency := flag.Int("concurrency", 4, "Number of concurrent workers")
	help := flag.Bool("help", false, "Show help information")
	timeout := flag.Duration("timeout", 180*time.Second, "Timeout for each URL")
	maxRetries := flag.Int("max-retries", 3, "Maximum number of retries for each URL")

	// 解析命令行参数
	flag.Parse()

	// 如果指定了 -help 参数，显示帮助信息并退出
	if *help {
		printHelp()
		os.Exit(0)
	}

	// 检查是否提供了文件路径
	if *urlFile == "" {
		fmt.Println("Please provide a file path using the -file flag")
		printHelp()
		os.Exit(1)
	}

	// 读取 URLs
	urls, err := readURLsFromFile(*urlFile)
	if err != nil {
		log.Fatalf("Error reading URLs from file: %v", err)
	}

	// 处理 URLs
	results := processURLsConcurrently(urls, *concurrency, *timeout, *maxRetries)

	// 计算汇总信息
	totalURLs := len(results)
	accessibleURLs := 0
	inaccessibleURLs := 0
	redirectedURLs := 0
	for _, result := range results {
		if result.StatusCode != -1 {
			accessibleURLs++
			if result.WasRedirected {
				redirectedURLs++
			}
		} else {
			inaccessibleURLs++
		}
	}

	// 打印汇总信息到控制台
	fmt.Printf("\n汇总信息:\n")
	fmt.Printf("总 URL 数: %d\n", totalURLs)
	fmt.Printf("可访问 URL 数: %d\n", accessibleURLs)
	fmt.Printf("无法访问 URL 数: %d\n", inaccessibleURLs)
	fmt.Printf("发生重定向的 URL 数: %d\n", redirectedURLs)

	// 生成 HTML 报告，传入汇总信息
	generateHTMLReport(results, totalURLs, accessibleURLs, inaccessibleURLs, redirectedURLs)

	// 清理 Chrome 进程
	cleanupChrome()
}

func printHelp() {
	fmt.Printf("URL Checker %s - A tool for checking the accessibility of multiple URLs\n\n", Version)
	fmt.Println("Usage: checkurl [options]")
	fmt.Println("\nOptions:")
	fmt.Println("  -file string")
	fmt.Println("        Path to the file containing URLs (required)")
	fmt.Println("  -concurrency int")
	fmt.Println("        Number of concurrent workers (default 4)")
	fmt.Println("  -timeout duration")
	fmt.Println("        Timeout for each URL (default 180s)")
	fmt.Println("  -max-retries int")
	fmt.Println("        Maximum number of retries for each URL (default 3)")
	fmt.Println("  -help")
	fmt.Println("        Show this help information")
	fmt.Println("\nExample:")
	fmt.Println("  checkurl -file urls.txt -concurrency 8 -timeout 240s -max-retries 5")
	fmt.Println("\nDescription:")
	fmt.Println("  This tool reads a list of URLs from a file, checks their accessibility,")
	fmt.Println("  captures screenshots, and generates an HTML report with the results.")
	fmt.Println("  The report includes the URL, title, status code, and screenshot for each accessible URL,")
	fmt.Println("  as well as a list of inaccessible URLs.")
	fmt.Println("  This tool handles URL redirects automatically.")
	fmt.Println("  The final URL after any redirects will be reported in the results.")
}

func readURLsFromFile(filename string) ([]string, error) {
	// 读取文件内容
	content, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}

	// 检测编码并转换为UTF-8
	content, err = convertToUTF8(content)
	if err != nil {
		return nil, err
	}

	// 将内容转换为字符串
	fileContent := string(content)

	// 分割成行并处理每一行
	var urls []string
	for _, line := range strings.Split(fileContent, "\n") {
		url := strings.TrimSpace(line)
		if url != "" {
			urls = append(urls, url)
		}
	}

	log.Printf("Read %d URLs from file", len(urls))

	return urls, nil
}

func convertToUTF8(content []byte) ([]byte, error) {
	if len(content) < 2 {
		return content, nil
	}

	var encoding string
	var err error

	switch {
	case content[0] == 0xFF && content[1] == 0xFE:
		// UTF-16 little endian
		encoding = "UTF-16LE"
		content, _, err = transform.Bytes(unicode.UTF16(unicode.LittleEndian, unicode.UseBOM).NewDecoder(), content)
	case content[0] == 0xFE && content[1] == 0xFF:
		// UTF-16 big endian
		encoding = "UTF-16BE"
		content, _, err = transform.Bytes(unicode.UTF16(unicode.BigEndian, unicode.UseBOM).NewDecoder(), content)
	default:
		// 假设是UTF-8
		encoding = "UTF-8"
	}

	if err != nil {
		return nil, err
	}

	log.Printf("Detected file encoding: %s", encoding)
	return content, nil
}

func processURLsConcurrently(urls []string, concurrency int, timeout time.Duration, maxRetries int) []Result {
	resultsChan := make(chan Result, len(urls))
	var wg sync.WaitGroup

	// 创建一个带缓冲的通道来限制并发数
	semaphore := make(chan struct{}, concurrency)

	for _, url := range urls {
		wg.Add(1)
		go func(url string) {
			defer wg.Done()
			semaphore <- struct{}{} // 获取信号量
			result := processURLWithRetry(url, maxRetries, timeout)
			resultsChan <- result
			<-semaphore // 释放信号量
		}(url)
	}

	go func() {
		wg.Wait()
		close(resultsChan)
	}()

	var results []Result
	for result := range resultsChan {
		results = append(results, result)
	}

	return results
}

func processURLWithRetry(url string, maxRetries int, timeout time.Duration) Result {
	var result Result
	for attempt := 1; attempt <= maxRetries; attempt++ {
		result = processURL(url, timeout)
		if result.StatusCode != -1 && result.Screenshot != "" {
			return result
		}
		log.Printf("Attempt %d failed for %s, retrying...", attempt, url)
		time.Sleep(time.Duration(attempt) * time.Second)
	}
	return result
}

func processURL(url string, timeout time.Duration) Result {
	result := Result{OriginalURL: url}
	url = ensureProtocol(url)

	// 获取状态码
	client := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return nil // 允许所有重定向
		},
	}
	resp, err := client.Get(url)
	if err != nil {
		log.Printf("HTTP request failed for %s: %v", url, err)
		result.StatusCode = -1
		return result // 直接返回，不进行后续处理
	}
	defer resp.Body.Close()

	result.StatusCode = resp.StatusCode
	result.URL = resp.Request.URL.String()
	// 移除可能添加的尾部斜杠，以进行更准确的比较
	originalURLWithoutSlash := strings.TrimSuffix(result.OriginalURL, "/")
	finalURLWithoutSlash := strings.TrimSuffix(result.URL, "/")

	// 比较去除协议和尾部斜杠后的 URL
	result.WasRedirected = !strings.EqualFold(
		strings.TrimPrefix(strings.TrimPrefix(originalURLWithoutSlash, "http://"), "https://"),
		strings.TrimPrefix(strings.TrimPrefix(finalURLWithoutSlash, "http://"), "https://"),
	)
	result.WasRedirected = (result.URL != result.OriginalURL)

	// 只有当 URL 可访问时，才进行截图和标题获取
	// 创建新的Chrome实例
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("ignore-certificate-errors", true),
		chromedp.Flag("disable-web-security", true),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-popup-blocking", true),
		chromedp.Flag("disable-extensions", true),
	)
	allocCtx, cancel := chromedp.NewExecAllocator(context.Background(), opts...)
	defer cancel()

	ctx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()

	ctx, cancel = context.WithTimeout(ctx, timeout)
	defer cancel()

	var buf []byte
	var title string
	var finalURL string
	err = chromedp.Run(ctx,
		chromedp.EmulateViewport(1280, 1024),
		network.Enable(),
		chromedp.Navigate(url),
		chromedp.ActionFunc(func(ctx context.Context) error {
			return waitForPageLoad(ctx)
		}),
		chromedp.Sleep(5*time.Second), // 添加额外的等待时间
		chromedp.ActionFunc(func(ctx context.Context) error {
			return waitForPageStable(ctx)
		}),
		chromedp.Location(&finalURL),
		chromedp.CaptureScreenshot(&buf),
		chromedp.Title(&title),
	)
	if err != nil {
		log.Printf("Failed to capture screenshot or title for %s: %v", url, err)
	}

	result.URL = finalURL
	result.WasRedirected = (result.URL != result.OriginalURL)

	if len(buf) > 0 {
		result.Screenshot = base64.StdEncoding.EncodeToString(buf)
		result.Title = title
		log.Printf("Screenshot captured for %s. Size: %d bytes", url, len(buf))
	} else {
		log.Printf("Screenshot buffer is empty for %s", url)
	}

	return result
}

func waitForPageLoad(ctx context.Context) error {
	return chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		_, exp, err := runtime.Evaluate(`
            new Promise((resolve, reject) => {
                if (document.readyState === 'complete') {
                    resolve();
                } else {
                    window.addEventListener('load', resolve);
                    setTimeout(reject, 30000);  // 30 seconds timeout
                }
            })
        `).Do(ctx)
		if err != nil {
			return err
		}
		if exp != nil {
			return fmt.Errorf("page load timed out: %v", exp)
		}
		return nil
	}))
}

func waitForPageStable(ctx context.Context) error {
	return chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		var lastHeight int
		for i := 0; i < 10; i++ { // 尝试 10 次
			var height int
			if err := chromedp.Evaluate(`document.body.scrollHeight`, &height).Do(ctx); err != nil {
				return err
			}
			if height == lastHeight {
				return nil // 页面高度稳定，认为加载完成
			}
			lastHeight = height
			time.Sleep(500 * time.Millisecond)
		}
		return nil
	}))
}

func ensureProtocol(url string) string {
	url = strings.TrimSpace(url)
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		// 尝试 HTTPS
		httpsURL := "https://" + url
		if checkURL(httpsURL) {
			url = httpsURL
		} else {
			// 如果 HTTPS 失败，尝试 HTTP
			httpURL := "http://" + url
			if checkURL(httpURL) {
				url = httpURL
			}
		}
	}

	// 如果 URL 不以斜杠结尾，添加斜杠
	if !strings.HasSuffix(url, "/") {
		url += "/"
	}

	return url
}

func checkURL(url string) bool {
	if !strings.HasSuffix(url, "/") {
		url += "/"
	}

	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
	resp, err := client.Get(url)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode < 400
}

func generateHTMLReport(results []Result, totalURLs, accessibleURLs, inaccessibleURLs, redirectedURLs int) {
	log.Printf("Generating report with: Total URLs: %d, Accessible: %d, Inaccessible: %d, Redirected: %d", totalURLs, accessibleURLs, inaccessibleURLs, redirectedURLs)

	summaryHTML := fmt.Sprintf(`
    <div class="summary">
        <h2>汇总信息</h2>
        <p>总 URL 数: %d</p>
        <p>可访问 URL 数: %d</p>
        <p>无法访问 URL 数: %d</p>
        <p>发生重定向的 URL 数: %d</p>
    </div>
    `, totalURLs, accessibleURLs, inaccessibleURLs, redirectedURLs)

	htmlContent := `
<!DOCTYPE html>
<html>
<head>
    <title>URL Check Results</title>
    <style>
        body {
            font-family: Arial, sans-serif;
            margin: 0;
            padding: 20px;
        }
        table {
            border-collapse: collapse;
            width: 100%;
            table-layout: auto;
        }
        th, td {
            border: 1px solid #ddd;
            padding: 8px;
            text-align: left;
            vertical-align: top;
            word-wrap: break-word;
        }
        th {
            background-color: #f2f2f2;
        }
        .screenshot {
            max-width: 50%;
            height: auto;
            cursor: pointer;
        }
        .fullscreen {
            position: fixed;
            top: 0;
            left: 0;
            width: 100%;
            height: 100%;
            background-color: rgba(0,0,0,0.9);
            display: flex;
            justify-content: center;
            align-items: center;
            z-index: 1000;
        }
        .fullscreen img {
            max-width: 90%;
            max-height: 90%;
            object-fit: contain;
        }
        .summary {
            background-color: #e6f3ff;
            padding: 10px;
            margin-bottom: 20px;
            border-radius: 5px;
        }
        .url-column {
            width: 30%;
        }
        .title-column {
            width: 30%;
        }
        .status-column {
            width: 10%;
        }
        .screenshot-column {
            width: 30%;
        }
        .redirected {
            color: orange;
        }
    </style>
</head>
<body>
    ` + summaryHTML + `
    <table>
        <tr>
            <th>序号</th>
            <th class="url-column">URL</th>
            <th class="title-column">标题</th>
            <th class="status-column">状态码</th>
            <th class="screenshot-column">截图</th>
        </tr>
`

	var inaccessibleURLsList []string
	accessibleCount := 0
	for _, result := range results {
		if result.StatusCode != -1 {
			accessibleCount++
			var screenshotHTML string
			if result.Screenshot != "" {
				screenshotHTML = fmt.Sprintf(`<img class="screenshot" src="data:image/png;base64,%s" alt="Screenshot" onclick="showFullscreen(this)">`, result.Screenshot)
			} else {
				screenshotHTML = "No screenshot available..."
			}

			redirectInfo := ""
			if result.WasRedirected {
				redirectInfo = fmt.Sprintf(`<br><span class="redirected">Redirected to: %s</span>`, result.URL)
			}

			htmlContent += fmt.Sprintf(`
        <tr>
            <td>%d</td>
            <td class="url-column">
                <a href="%s" target="_blank">%s</a>
                %s
            </td>
            <td class="title-column">%s</td>
            <td class="status-column">%d</td>
            <td class="screenshot-column">
                %s
            </td>
        </tr>
`, accessibleCount, result.URL, result.OriginalURL, redirectInfo, result.Title, result.StatusCode, screenshotHTML)
		} else {
			inaccessibleURLsList = append(inaccessibleURLsList, result.OriginalURL)
		}
	}

	htmlContent += `
    </table>
`

	// 添加无法访问的 URL 列表
	if len(inaccessibleURLsList) > 0 {
		htmlContent += `
    <h2>无法访问的 URL 列表</h2>
    <ol>
`
		for _, url := range inaccessibleURLsList {
			htmlContent += fmt.Sprintf("        <li>%s</li>\n", url)
		}
		htmlContent += `
    </ol>
`
	}

	htmlContent += `
    <div id="fullscreenContainer" class="fullscreen" style="display: none;" onclick="this.style.display='none';">
        <img id="fullscreenImage" src="" alt="Fullscreen Screenshot">
    </div>
    <script>
        function showFullscreen(img) {
            var fullscreenContainer = document.getElementById('fullscreenContainer');
            var fullscreenImage = document.getElementById('fullscreenImage');
            fullscreenImage.src = img.src;
            fullscreenContainer.style.display = 'flex';
        }
    </script>
</body>
</html>
`

	err := os.WriteFile("results.html", []byte(htmlContent), 0644)
	if err != nil {
		log.Fatal("Failed to save HTML report:", err)
	}

	fmt.Println("Results saved to results.html")
}

func cleanupChrome() {
	var cmd *exec.Cmd
	switch goruntime.GOOS {
	case "windows":
		cmd = exec.Command("taskkill", "/F", "/IM", "chrome.exe")
	case "darwin":
		cmd = exec.Command("pkill", "Chrome")
	default: // linux and others
		cmd = exec.Command("pkill", "chrome")
	}

	err := cmd.Run()
	if err != nil {
		log.Printf("Failed to kill Chrome processes: %v", err)
	} else {
		log.Println("Successfully cleaned up Chrome processes")
	}
}
