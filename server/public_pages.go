package server

import (
	"bytes"
	"html/template"
	"net/http"
)

// Public, no-login, no-JS pages (/about, /privacy, /terms).
//
// The SPA answers every URL with the same JS shell, which a crawler (Google's
// OAuth brand verification, search engines, link previews) sees as an empty
// page behind a login wall. These pages are plain server-rendered HTML so the
// app's purpose, its privacy policy and its terms are readable by anything that
// can fetch a URL.

// publicAppName is the name shown on the public pages and expected to match
// the OAuth consent-screen name: DWORKSPACE_PUBLIC_APP_NAME, then the
// instance_name setting, then "dworkspace".
func (s *Server) publicAppName() string {
	if v := Env("PUBLIC_APP_NAME"); v != "" {
		return v
	}
	if v := s.setting("instance_name", ""); v != "" {
		return v
	}
	return "dworkspace"
}

type publicPageData struct {
	Name    string
	Contact string
	Page    string
	Base    string
}

const publicEffectiveDate = "2026-11-04"

var publicTmpl = template.Must(template.New("public").Parse(`<!doctype html>
<html lang="vi">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Name}}</title>
<meta name="description" content="{{.Name}} - không gian làm việc nhóm: trang, cơ sở dữ liệu, tài liệu, bình luận / team workspace: pages, databases, documents, comments.">
<style>
body{font:16px/1.6 system-ui,sans-serif;max-width:46rem;margin:0 auto;padding:1.5rem;color:#222}
nav a{margin-right:1rem}
h1{margin-top:1.5rem}
h2{margin-top:2rem;font-size:1.2rem}
hr{margin:2.5rem 0;border:0;border-top:1px solid #ccc}
footer{margin-top:3rem;color:#666;font-size:.9rem}
</style>
</head>
<body>
<header>
<nav>
<a href="/about">About</a>
<a href="/privacy">Privacy</a>
<a href="/terms">Terms</a>
<a href="/">Sign in / Đăng nhập</a>
</nav>
<h1>{{.Name}}</h1>
</header>
<main>
{{if eq .Page "about"}}{{template "about" .}}{{else if eq .Page "privacy"}}{{template "privacy" .}}{{else}}{{template "terms" .}}{{end}}
</main>
<footer>
<a href="/about">About</a> · <a href="/privacy">Privacy Policy</a> · <a href="/terms">Terms of Service</a>
{{if .Contact}}<br>Contact / Liên hệ: <a href="mailto:{{.Contact}}">{{.Contact}}</a>{{end}}
</footer>
</body>
</html>
{{define "about"}}
<h2>Giới thiệu</h2>
<p>{{.Name}} là không gian làm việc nhóm tự lưu trữ: trang tài liệu, cơ sở dữ liệu dạng bảng, văn bản, bình luận và cộng tác theo thời gian thực. Tác nhân AI (AI agent) có thể truy cập không gian làm việc thông qua giao thức MCP, với đúng quyền mà người dùng cấp cho tác nhân đó.</p>
<h2>Dành cho ai</h2>
<p>Các nhóm và tổ chức cần một nơi chung để ghi chép, quản lý thông tin và làm việc cùng nhau. Dịch vụ không dành cho công chúng: chỉ những người được mời hoặc được quản trị viên cho phép mới tạo được tài khoản.</p>
<h2>Đăng nhập</h2>
<p>Bạn đăng nhập bằng tài khoản Google hoặc Microsoft, hoặc bằng email và mật khẩu nếu quản trị viên bật tính năng này. Xem <a href="/privacy">Chính sách quyền riêng tư</a> và <a href="/terms">Điều khoản sử dụng</a>.</p>
<hr>
<h2>About</h2>
<p>{{.Name}} is a self-hosted team workspace: pages, databases, documents, comments and real-time collaboration. AI agents can access the workspace through the Model Context Protocol (MCP), with exactly the permissions the user grants them.</p>
<h2>Who it is for</h2>
<p>Teams and organisations that need a shared place to write, organise information and work together. The service is not open to the general public: only invited people or those allowed by an administrator can create an account.</p>
<h2>Sign-in</h2>
<p>You sign in with a Google or Microsoft account, or with email and password if the administrator enables it. See the <a href="/privacy">Privacy Policy</a> and the <a href="/terms">Terms of Service</a>.</p>
{{end}}
{{define "privacy"}}
<p><em>Có hiệu lực từ / Effective: ` + publicEffectiveDate + `</em></p>
<h2>1. Dữ liệu chúng tôi thu thập</h2>
<ul>
<li>Thông tin hồ sơ tài khoản khi bạn đăng nhập bằng Google hoặc Microsoft: tên, địa chỉ email, ảnh đại diện.</li>
<li>Nội dung bạn tạo trong không gian làm việc: trang, cơ sở dữ liệu, tài liệu, bình luận.</li>
<li>Tệp bạn tải lên.</li>
<li>Nhật ký kỹ thuật và nhật ký hoạt động (thời gian, địa chỉ IP, thao tác) phục vụ bảo mật và vận hành.</li>
</ul>
<h2>2. Cách sử dụng dữ liệu</h2>
<p>Dữ liệu chỉ được dùng để cung cấp và vận hành dịch vụ {{.Name}}: xác thực bạn, hiển thị nội dung cho đúng người được chia sẻ, gửi thông báo và bảo vệ hệ thống khỏi lạm dụng.</p>
<h2>3. Dữ liệu người dùng Google</h2>
<ul>
<li><code>openid</code>, <code>email</code>, <code>profile</code>: dùng để đăng nhập, xác định tài khoản và hiển thị tên, email, ảnh đại diện của bạn.</li>
<li><code>https://www.googleapis.com/auth/gmail.send</code>: chỉ được yêu cầu khi quản trị viên kết nối thư gửi đi của hệ thống. Quyền này chỉ dùng để gửi email thông báo của chính hệ thống này. Chúng tôi không đọc, liệt kê hay lưu nội dung hộp thư.</li>
</ul>
<p>Việc sử dụng và chuyển giao thông tin nhận được từ Google APIs sang bất kỳ ứng dụng nào khác sẽ tuân thủ <a href="https://developers.google.com/terms/api-services-user-data-policy">Chính sách Dữ liệu Người dùng của Google API Services</a>, bao gồm các yêu cầu Sử dụng Hạn chế (Limited Use).</p>
<h2>4. Không bán, không chia sẻ, không quảng cáo</h2>
<p>Chúng tôi không bán dữ liệu của bạn, không chia sẻ cho bên quảng cáo hay môi giới dữ liệu và không dùng dữ liệu để hiển thị quảng cáo. Dữ liệu người dùng Google không được dùng để huấn luyện mô hình AI.</p>
<h2>5. Lưu trữ và bảo mật</h2>
<p>Dữ liệu được lưu trên máy chủ do đơn vị vận hành quản lý, truyền qua HTTPS. Mật khẩu được băm; mã truy cập OAuth được lưu trên máy chủ và chỉ truy cập được bởi hệ thống. Truy cập vào dữ liệu được giới hạn theo quyền của từng không gian làm việc.</p>
<h2>6. Lưu giữ và xoá dữ liệu</h2>
<p>Dữ liệu được lưu cho đến khi bạn hoặc quản trị viên xoá. Để yêu cầu xoá tài khoản và dữ liệu liên quan, hoặc thu hồi quyền Google, hãy liên hệ quản trị viên hoặc email liên hệ bên dưới; chúng tôi xử lý trong vòng 30 ngày. Bạn cũng có thể thu hồi quyền truy cập tại <a href="https://myaccount.google.com/permissions">myaccount.google.com/permissions</a>.</p>
<h2>7. Bên thứ ba</h2>
<p>Chúng tôi dùng nhà cung cấp hạ tầng lưu trữ và mạng để chạy dịch vụ. Họ chỉ xử lý dữ liệu để cung cấp hạ tầng, không dùng cho mục đích riêng.</p>
<h2>8. Thay đổi</h2>
<p>Chính sách này có thể được cập nhật; ngày hiệu lực ở đầu trang sẽ thay đổi tương ứng.</p>
<h2>9. Liên hệ</h2>
<p>{{if .Contact}}<a href="mailto:{{.Contact}}">{{.Contact}}</a>{{else}}Vui lòng liên hệ quản trị viên của {{.Name}}.{{end}}</p>
<hr>
<h2>Privacy Policy</h2>
<p><em>Effective: ` + publicEffectiveDate + `</em></p>
<h2>1. Data we collect</h2>
<ul>
<li>Account profile information when you sign in with Google or Microsoft: name, email address, avatar.</li>
<li>Workspace content you create: pages, databases, documents, comments.</li>
<li>Files you upload.</li>
<li>Technical and activity logs (time, IP address, actions) for security and operations.</li>
</ul>
<h2>2. How we use data</h2>
<p>Data is used only to provide and operate {{.Name}}: to authenticate you, show content to the people it is shared with, send notifications and protect the system from abuse.</p>
<h2>3. Google user data</h2>
<ul>
<li><code>openid</code>, <code>email</code>, <code>profile</code>: used for sign-in, to identify your account and to show your name, email and avatar.</li>
<li><code>https://www.googleapis.com/auth/gmail.send</code>: requested only when an administrator connects outgoing mail. It is used solely to send this instance's own notification emails. We never read, list or store mailbox content.</li>
</ul>
<p>{{.Name}}'s use and transfer to any other app of information received from Google APIs will adhere to the <a href="https://developers.google.com/terms/api-services-user-data-policy">Google API Services User Data Policy</a>, including the Limited Use requirements.</p>
<h2>4. No selling, no sharing, no ads</h2>
<p>We do not sell your data, share it with advertisers or data brokers, or use it for advertising. Google user data is not used to train AI models.</p>
<h2>5. Storage and security</h2>
<p>Data is stored on servers managed by the operator and transmitted over HTTPS. Passwords are hashed; OAuth tokens are stored server-side and accessible only to the system. Access to data is limited by workspace permissions.</p>
<h2>6. Retention and deletion</h2>
<p>Data is kept until you or an administrator delete it. To request deletion of your account and related data, or to revoke Google access, contact your administrator or the contact email below; we process requests within 30 days. You can also revoke access at <a href="https://myaccount.google.com/permissions">myaccount.google.com/permissions</a>.</p>
<h2>7. Third parties</h2>
<p>We use hosting and network infrastructure providers to run the service. They process data only to provide that infrastructure, not for their own purposes.</p>
<h2>8. Changes</h2>
<p>This policy may be updated; the effective date above changes accordingly.</p>
<h2>9. Contact</h2>
<p>{{if .Contact}}<a href="mailto:{{.Contact}}">{{.Contact}}</a>{{else}}Please contact the administrator of {{.Name}}.{{end}}</p>
{{end}}
{{define "terms"}}
<p><em>Có hiệu lực từ / Effective: ` + publicEffectiveDate + `</em></p>
<h2>Điều khoản sử dụng</h2>
<ol>
<li>Bằng việc sử dụng {{.Name}}, bạn đồng ý với các điều khoản này và <a href="/privacy">Chính sách quyền riêng tư</a>.</li>
<li>Bạn chịu trách nhiệm về tài khoản của mình và nội dung bạn tạo; không dùng dịch vụ cho mục đích bất hợp pháp, phát tán mã độc hoặc xâm phạm quyền của người khác.</li>
<li>Quản trị viên có thể tạm khoá hoặc xoá tài khoản vi phạm điều khoản.</li>
<li>Bạn giữ quyền sở hữu nội dung của mình. Chúng tôi chỉ xử lý nội dung để vận hành dịch vụ.</li>
<li>Dịch vụ được cung cấp "nguyên trạng", không có bảo đảm về tính liên tục hay không có lỗi; trong phạm vi pháp luật cho phép, chúng tôi không chịu trách nhiệm về thiệt hại gián tiếp. Hãy sao lưu dữ liệu quan trọng.</li>
<li>Điều khoản có thể được cập nhật; tiếp tục sử dụng nghĩa là bạn chấp nhận bản cập nhật.</li>
</ol>
<hr>
<h2>Terms of Service</h2>
<ol>
<li>By using {{.Name}} you agree to these terms and to the <a href="/privacy">Privacy Policy</a>.</li>
<li>You are responsible for your account and the content you create; do not use the service for unlawful purposes, to distribute malware or to infringe others' rights.</li>
<li>Administrators may suspend or delete accounts that violate these terms.</li>
<li>You retain ownership of your content. We process it only to operate the service.</li>
<li>The service is provided "as is", without warranty of availability or freedom from errors; to the extent permitted by law we are not liable for indirect damages. Back up important data.</li>
<li>These terms may be updated; continued use means you accept the update.</li>
</ol>
{{end}}`))

func (s *Server) publicPage(page string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			httpError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var buf bytes.Buffer
		err := publicTmpl.Execute(&buf, publicPageData{
			Name:    s.publicAppName(),
			Contact: Env("PUBLIC_CONTACT_EMAIL"),
			Page:    page,
		})
		if err != nil {
			httpError(w, http.StatusInternalServerError, "render failed")
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=300")
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodGet {
			w.Write(buf.Bytes())
		}
	}
}

func (s *Server) registerPublicPages(m *http.ServeMux) {
	m.HandleFunc("/about", s.publicPage("about"))
	m.HandleFunc("/privacy", s.publicPage("privacy"))
	m.HandleFunc("/terms", s.publicPage("terms"))
}
