# CheckURL

CheckURL 是一个强大的 URL 检查工具，可以并发处理多个 URL，获取网页标题、状态码，并捕获网页截图。

## 功能

- 从文件中读取 URL 列表
- 并发处理多个 URL
- 获取网页标题和 HTTP 状态码
- 捕获网页截图
- 生成包含结果的 HTML 报告
- 自动处理不同的文件编码（UTF-8, UTF-16LE, UTF-16BE）
- 自动添加 HTTP/HTTPS 协议（如果 URL 中未指定）
- 处理 SSL 证书错误
- 尝试关闭可能的弹窗以获取更好的截图

## 安装

确保您的系统上安装了 Go（推荐 Go 1.16 或更高版本）。然后，克隆此仓库：

```bash
git clone https://github.com/yourusername/CheckURL.git
cd CheckURL
```
## 使用方法
1.准备一个包含 URL 列表的文本文件，每行一个 URL。

2.运行程序：
```bash
  go run main.go -file path/to/your/url_file.txt -concurrency 4
```
参数说明：
- -file: 指定包含 URL 列表的文件路径（必需）
- -concurrency: 指定并发处理的数量（可选，默认为 4）


3.程序执行完毕后，将在当前目录生成一个 results.html 文件，包含所有处理结果。

## 输出
程序将生成一个 HTML 报告（results.html），其中包含：

1.每个 URL 的序号
2.URL 链接
3.网页标题
4.HTTP 状态码
5.网页截图（可点击查看大图）
## 注意事项
1.本程序使用 Chrome 浏览器进行网页渲染和截图，请确保系统中安装了 Chrome。
2.程序会自动处理 SSL 证书错误，但这可能带来安全风险，请谨慎使用。
3.对于某些复杂的网页或需要认证的网页，可能无法正确获取截图或标题。
## 依赖
- github.com/chromedp/cdproto
- github.com/chromedp/chromedp
- golang.org/x/text