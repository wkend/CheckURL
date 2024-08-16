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

type Result struct {
	URL        string
	Title      string
	StatusCode int
	Screenshot string
	Accessible bool
}

func main() {
	// 定义命令行标志
	urlFile := flag.String("file", "", "Path to the file containing URLs")
	concurrency := flag.Int("concurrency", 4, "Number of concurrent workers")

	// 解析命令行参数
	flag.Parse()

	// 检查是否提供了文件路径
	if *urlFile == "" {
		fmt.Println("Please provide a file path using the -file flag")
		flag.PrintDefaults()
		os.Exit(1)
	}

	// 读取 URLs
	urls, err := readURLsFromFile(*urlFile)
	if err != nil {
		log.Fatalf("Error reading URLs from file: %v", err)
	}

	// 处理 URLs
	results := processURLsConcurrently(urls, *concurrency)
	generateHTMLReport(results)

	// 打印汇总信息
	inaccessibleCount := 0
	for _, result := range results {
		if !result.Accessible {
			inaccessibleCount++
		}
	}
	fmt.Printf("\n汇总信息:\n")
	fmt.Printf("总 URL 数: %d\n", len(results))
	fmt.Printf("可访问 URL 数: %d\n", len(results)-inaccessibleCount)
	fmt.Printf("无法访问 URL 数: %d\n", inaccessibleCount)

	// 清理 Chrome 进程
	cleanupChrome()
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

func processURLsConcurrently(urls []string, concurrency int) []Result {
	resultsChan := make(chan Result, len(urls))
	var wg sync.WaitGroup

	// 创建一个带缓冲的通道来限制并发数
	semaphore := make(chan struct{}, concurrency)

	for _, url := range urls {
		wg.Add(1)
		go func(url string) {
			defer wg.Done()
			semaphore <- struct{}{} // 获取信号量
			result := processURL(url)
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

func processURL(url string) Result {
	result := Result{URL: url}

	url = ensureProtocol(url)

	// 获取状态码
	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
	resp, err := client.Get(url)
	if err != nil {
		log.Printf("HTTP request failed for %s: %v", url, err)
		result.StatusCode = -1
		result.Accessible = false
		fmt.Printf("无法访问的 URL: %s\n", url) // 在控制台打印信息
	} else {
		result.StatusCode = resp.StatusCode
		result.URL = resp.Request.URL.String()
		resp.Body.Close()
	}

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

	ctx, cancel = context.WithTimeout(ctx, 120*time.Second) // 增加超时时间到120秒
	defer cancel()

	var buf []byte
	var title string
	err = chromedp.Run(ctx,
		chromedp.EmulateViewport(1280, 1024),
		network.Enable(),
		chromedp.Navigate(url),
		chromedp.ActionFunc(func(ctx context.Context) error {
			return waitForPageLoad(ctx)
		}),
		chromedp.ActionFunc(func(ctx context.Context) error {
			// 尝试关闭可能的弹窗
			_ = chromedp.Run(ctx, chromedp.Evaluate(`
                var closeButtons = document.querySelectorAll('button, [role="button"]');
                for (var i = 0; i < closeButtons.length; i++) {
                    if (closeButtons[i].textContent.toLowerCase().includes('close') || 
                        (closeButtons[i].getAttribute('aria-label') && closeButtons[i].getAttribute('aria-label').toLowerCase().includes('close'))) {
                        closeButtons[i].click();
                        break;
                    }
                }
            `, nil))
			return nil
		}),
		chromedp.CaptureScreenshot(&buf),
		chromedp.Title(&title),
	)
	if err != nil {
		log.Printf("Failed to capture screenshot or title for %s: %v", url, err)
		// 尝试再次捕获截图，但这次不等待页面完全加载
		err = chromedp.Run(ctx,
			chromedp.CaptureScreenshot(&buf),
			chromedp.Title(&title),
		)
		if err != nil {
			log.Printf("Failed to capture screenshot or title for %s: %v", url, err)
			result.Accessible = false
			fmt.Printf("无法获取截图或标题的 URL: %s\n", url) // 在控制台打印信息
		}
	}

	if len(buf) > 0 {
		result.Screenshot = base64.StdEncoding.EncodeToString(buf)
		result.Title = title
		log.Printf("Screenshot captured for %s. Size: %d bytes", url, len(buf))
	} else {
		log.Printf("Screenshot buffer is empty for %s", url)
		result.Screenshot = "" // 确保设置为空字符串，而不是nil
	}

	// 确保Chrome实例被关闭
	if err := chromedp.Cancel(ctx); err != nil {
		log.Printf("Error closing Chrome for %s: %v", url, err)
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

func ensureProtocol(url string) string {
	if strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://") {
		return url
	}

	// 尝试 HTTPS
	httpsURL := "https://" + url
	if checkURL(httpsURL) {
		return httpsURL
	}

	// 如果 HTTPS 失败，尝试 HTTP
	httpURL := "http://" + url
	if checkURL(httpURL) {
		return httpURL
	}

	// 如果两者都失败，返回原始 URL
	return url
}

func checkURL(url string) bool {
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

func generateHTMLReport(results []Result) {
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
        }
        th, td {
            border: 1px solid #ddd;
            padding: 8px;
            text-align: left;
            vertical-align: top;
        }
        th {
            background-color: #f2f2f2;
        }
        .screenshot {
            max-width: 100%;
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
        .inaccessible {
            background-color: #ffcccc;
        }
    </style>
</head>
<body>
    <h1>URL Check Results</h1>
    <table>
        <tr>
            <th>序号</th>
            <th>URL</th>
            <th>标题</th>
            <th>状态码</th>
            <th>截图</th>
        </tr>
`

	var inaccessibleURLs []string
	for i, result := range results {
		var screenshotHTML string
		if result.Screenshot != "" {
			screenshotHTML = fmt.Sprintf(`<img class="screenshot" src="data:image/png;base64,%s" alt="Screenshot" onclick="showFullscreen(this)">`, result.Screenshot)
		} else {
			screenshotHTML = "No screenshot available"
		}

		rowClass := ""
		if !result.Accessible {
			rowClass = ` class="inaccessible"`
			inaccessibleURLs = append(inaccessibleURLs, result.URL)
		}

		htmlContent += fmt.Sprintf(`
        <tr%s>
            <td>%d</td>
            <td><a href="%s" target="_blank">%s</a></td>
            <td>%s</td>
            <td>%d</td>
            <td>
                %s
            </td>
        </tr>
`, rowClass, i+1, result.URL, result.URL, result.Title, result.StatusCode, screenshotHTML)
	}

	htmlContent += `
    </table>
`

	// 添加无法访问的 URL 列表
	if len(inaccessibleURLs) > 0 {
		htmlContent += `
    <h2>无法访问的 URL 列表</h2>
    <ul>
`
		for _, url := range inaccessibleURLs {
			htmlContent += fmt.Sprintf("        <li>%s</li>\n", url)
		}
		htmlContent += `
    </ul>
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
	fmt.Printf("Total URLs: %d, Inaccessible URLs: %d\n", len(results), len(inaccessibleURLs))
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
