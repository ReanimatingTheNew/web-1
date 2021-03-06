package web

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/VectorsOrigin/template"
	"github.com/VectorsOrigin/utils"
)

/*
	Handler 负责处理控制器Request,Response的数据处理和管理

*/

const (
	HANDLER_VER = "1.2.0"
)

var (
	cookieNameSanitizer  = strings.NewReplacer("\n", "-", "\r", "-")
	cookieValueSanitizer = strings.NewReplacer("\n", " ", "\r", " ", ";", " ")

	// onExitFlushLoop is a callback set by tests to detect the state of the
	// flushLoop() goroutine.
	onExitFlushLoop func()
	hopHeaders      = []string{
		"Connection",
		"Proxy-Connection", // non-standard but still sent by libcurl and rejected by e.g. google
		"Keep-Alive",
		"Proxy-Authenticate",
		"Proxy-Authorization",
		"Te",      // canonicalized version of "TE"
		"Trailer", // not Trailers per URL above; http://www.rfc-editor.org/errata_search.php?eid=4522
		"Transfer-Encoding",
		"Upgrade",
	}
)

// A BufferPool is an interface for getting and returning temporary
// byte slices for use by io.CopyBuffer.
type BufferPool interface {
	Get() []byte
	Put([]byte)
}

type writeFlusher interface {
	io.Writer
	http.Flusher
}

type maxLatencyWriter struct {
	dst     writeFlusher
	latency time.Duration

	mu   sync.Mutex // protects Write + Flush
	done chan bool
}
type (
	// 用来提供API格式化Body
	TContentBody struct {
		handler *THandler
		data    []byte //#**** 由于读完Body被清空所以必须再次保存样本
	}

	TParamsSet struct {
		handler *THandler
		params  map[string]string
		name    string
	}

	// THandler 负责所有请求任务,每个Handle表示有一个请求
	THandler struct {
		IResponseWriter
		Response IResponseWriter //http.ResponseWriter
		Request  *http.Request   //

		Router *TRouter
		Route  *TRoute //执行本次Handle的Route
		//Logger   *logger.TLogger
		Template *template.TTemplateSet // 概念改进  为Hd添加 hd.Template.Params.Set("模板数据",Val)/Get()/Del()

		//PostBody []byte          //废弃 Post 整体数据

		//GET          map[string]string //废弃
		//POST         map[string]string //废弃
		COOKIE       map[string]string //
		methodParams *TParamsSet       //map[string]string // Post Get 传递回来的参数
		pathParams   *TParamsSet       //map[string]string // Url 传递回来的参数
		body         *TContentBody

		// 模板
		TemplateSrc string                 // 模板名称
		RenderArgs  map[string]interface{} // TODO (name TemplateData) Args passed to the template.

		// 返回
		ContentType string
		Data        map[string]interface{} // 数据缓存在各个Controler间调用
		Result      []byte                 // 最终返回数据由Apply提交

		CtrlIndex int // -- 提示目前控制器Index
		//CtrlCount int           // --
		isApplies bool          // -- 已经提交过
		finalCall reflect.Value // -- handler 结束执行的动作处理器
		val       reflect.Value
	}

	// 反向代理
	TProxyHandler struct {
		IResponseWriter
		Response IResponseWriter //http.ResponseWriter
		Request  *http.Request   //

		Router *TRouter
		Route  *TRoute //执行本次Handle的Route

		//Logger *logger.TLogger

		// Director must be a function which modifies
		// the request into a new request to be sent
		// using Transport. Its response is then copied
		// back to the original client unmodified.
		Director func(*http.Request)

		// The transport used to perform proxy requests.
		// If nil, http.DefaultTransport is used.
		Transport http.RoundTripper

		// FlushInterval specifies the flush interval
		// to flush to the client while copying the
		// response body.
		// If zero, no periodic flushing is done.
		FlushInterval time.Duration
		// BufferPool optionally specifies a buffer pool to
		// get byte slices for use by io.CopyBuffer when
		// copying HTTP response bodies.
		BufferPool BufferPool
		// ModifyResponse is an optional function that
		// modifies the Response from the backend.
		// If it returns an error, the proxy returns a StatusBadGateway error.
		ModifyResponse func(*http.Response) error
	}
)

// 100%返回TContentBody
func NewContentBody(hd *THandler) (res *TContentBody) {
	res = &TContentBody{
		handler: hd,
	}
	body, err := ioutil.ReadAll(hd.Request.Body)
	if err != nil {
		logger.Err("Read request body faild with an error : %s!", err.Error())
	}

	res.data = body
	return
}

func NewParamsSet(hd *THandler) *TParamsSet {
	return &TParamsSet{
		handler: hd,
		params:  make(map[string]string),
	}
}

func NewHandler() *THandler {
	//func NewHandler(router *TRouter, route *TRoute, writer iResponseWriter, request *http.Request) *THandler {
	hd := &THandler{
		//Router:          router,
		//Route:           route,
		//iResponseWriter: writer,
		//Response:        writer,
		//Request:         request,
		//GET:    map[string]string{},
		//POST:   map[string]string{},
		COOKIE: map[string]string{},
		//		SESSION:      map[string]interface{}{},
		//MethodParams: map[string]string{},
		//PathParams:   map[string]string{},
		RenderArgs: make(map[string]interface{}),
		Data:       make(map[string]interface{}),
	} // 这个handle将传递给 请求函数的头一个参数func test(hd *webgo.THandler) {}
	//hd.Update(writer, request)

	// 必须不为nil
	//	hd.MethodParams=NewParamsSet(hd)
	hd.pathParams = NewParamsSet(hd)
	hd.val = reflect.ValueOf(hd)
	return hd
}

func NewProxyHandler() *TProxyHandler {
	hd := &TProxyHandler{}

	return hd
}

func (self *TContentBody) AsBytes() []byte {

	return self.data
}

// Body 必须是Json结构才能你转
func (self *TContentBody) AsMap() (result map[string]interface{}) {
	result = make(map[string]interface{})
	err := json.Unmarshal(self.data, &result)
	if err != nil {
		logger.Err(err.Error())
		return nil
	}
	return
}

func (self *TParamsSet) AsString(name string) string {
	return self.params[name]
}

func (self *TParamsSet) AsInteger(name string) int64 {
	return utils.StrToInt64(self.params[name])
}

func (self *TParamsSet) AsBoolean(name string) bool {
	return utils.StrToBool(self.params[name])
}

func (self *TParamsSet) AsDateTime(name string) (t time.Time) {
	t, _ = time.Parse(time.RFC3339, self.params[name])
	return
}

func (self *TParamsSet) AsFloat(name string) float64 {
	return utils.StrToFloat(self.params[name])
}

// Call in the end of all controller
func (self *THandler) FinalCall(aFunc func(*THandler)) {
	self.finalCall = reflect.ValueOf(aFunc)
}

/*
func (self *THandler) getPathParams() {
	// 获得正则字符做为Handler参数
	lSubmatch := self.Route.regexp.FindStringSubmatch(self.Request.URL.Path) //更加深层次正则表达式比对nil 为没有
	if lSubmatch != nil && lSubmatch[0] == self.Request.URL.Path {           // 第一个是Match字符串本身
		for i, arg := range lSubmatch[1:] { ///Url正则字符作为参数
			if arg != "" {
				self.PathParams[self.Route.regexp.SubexpNames()[i+1]] = arg //SubexpNames 获得URL 上的(?P<keywords>.*)
			}
		}
	}
}
*/

//TODO 添加验证Request 防止多次解析
func (self *THandler) MethodParams() *TParamsSet {
	if self.methodParams == nil {
		self.methodParams = NewParamsSet(self)
	}

	// 获得GET
	q := self.Request.URL.Query()
	for key, _ := range q {
		//Debug("key:", key)
		self.methodParams.params[key] = q.Get(key)
	}

	// 获得POST
	ct := self.Request.Header.Get("Content-Type")
	ct, _, _ = mime.ParseMediaType(ct)
	if ct == "multipart/form-data" {
		self.Request.ParseMultipartForm(256)
	} else {
		self.Request.ParseForm() //#Go通过r.ParseForm之后，把用户POST和GET的数据全部放在了r.Form里面
	}

	for key, _ := range self.Request.Form {
		//Debug("key2:", key)
		self.methodParams.params[key] = self.Request.FormValue(key)
	}

	//self.PostBody, _ = ioutil.ReadAll(self.Request.Body) // 返回Body 但POST 的时候
	//fmt.Println("PostBody", string(self.PostBody))

	return self.methodParams
}

// 如果返回 nil 代表 Url 不含改属性
func (self *THandler) PathParams() *TParamsSet {
	return self.pathParams
}

func (self *THandler) Body() *TContentBody {
	if self.body == nil {
		self.body = NewContentBody(self)
	}

	//self.Request.Body.Close()
	//self.Request.Body = ioutil.NopCloser(bytes.NewBuffer(body))
	return self.body
}

// 值由调用创建
func (self *THandler) _setMethodParams(MAX_FORM_SIZE int64) {
	if self.methodParams == nil {
		self.methodParams = NewParamsSet(self)
	}

	// 获得GET
	q := self.Request.URL.Query()
	for key, _ := range q {
		self.methodParams.params[key] = q.Get(key)
	}

	// 获得POST
	ct := self.Request.Header.Get("Content-Type")
	ct, _, _ = mime.ParseMediaType(ct)
	if ct == "multipart/form-data" {
		self.Request.ParseMultipartForm(MAX_FORM_SIZE)
	} else {
		self.Request.ParseForm() //#Go通过r.ParseForm之后，把用户POST和GET的数据全部放在了r.Form里面
	}

	for key, _ := range self.Request.Form {
		self.methodParams.params[key] = self.Request.FormValue(key)
	}

	//self.PostBody, _ = ioutil.ReadAll(self.Request.Body) // 返回Body 但POST 的时候
	//fmt.Println("PostBody", string(self.PostBody))
	return
}

// 值由Router 赋予
func (self *THandler) setPathParams(name, val string) {
	self.pathParams.params[name] = val
}

/*
func (self *THandler) UpdateSession() {
	self.Router.Sessions.UpdateSession(self.COOKIE[self.Router.Sessions.CookieName], self.SESSION)
}
*/

/*
刷新
#刷新Handler的新请求数据
*/
// Inite and Connect a new ResponseWriter when a new request is coming
func (self *THandler) connect(rw IResponseWriter, req *http.Request, Router *TRouter, Route *TRoute) {
	self.Request = req
	self.Response = rw
	self.IResponseWriter = rw
	self.Router = Router
	self.Route = Route
	//self.Logger = Router.Server.Logger
	self.Template = Router.Template
	self.TemplateSrc = ""
	self.ContentType = ""
	self.RenderArgs = make(map[string]interface{}) // 清空
	self.Data = make(map[string]interface{})       // 清空
	self.body = nil
	self.Result = nil
	self.CtrlIndex = 0 // -- 提示目前控制器Index
	//self.CtrlCount = 0     // --
	self.isApplies = false // -- 已经提交过

	//CookieSessions.ConnectSession(rw, req)
	//MemorySessions.ConnectSession(rw, req)
	//self.getPathParams()     // 获得Path[请求参数]
	//self.getMethodParams(32) //废弃 #获得Form[请求参数]
	//self.GetCookie()
	//self.finalCall = reflect.Zero(self.finalCall.Type()) // -- handler 结束执行的动作处理器
}

// 执行所以变动
func (self *THandler) Apply() {
	if !self.isApplies {
		// 如果有模板文件输入
		if self.TemplateSrc != "" {
			self.SetHeader(true, "Content-Type", self.ContentType)
			//self.Template.Render(self.TemplateSrc, self.Response, self.RenderArgs)
			err := self.Template.RenderToWriter(self.TemplateSrc, self.RenderArgs, self.Response, "base")
			if err != nil {
				http.Error(self.Response, "Apply fail:"+err.Error(), http.StatusInternalServerError)
			}
		} else if !self.Response.Written() { // STEP:只许一次返回
			self.Write(self.Result)
		}

		self.isApplies = true
	}

	return
}

func (self *THandler) CtrlCount() int {
	return len(self.Route.Ctrls)
}
func (self *THandler) IP() (res []string) {
	ip := strings.Split(self.Request.RemoteAddr, ":")
	if len(ip) > 0 {
		if ip[0] != "[" {
			res = append(res, ip[0])
		}
	}

	proxy := make([]string, 0)
	if ips := self.Request.Header.Get("X-Forwarded-For"); ips != "" {
		proxy = strings.Split(ips, ",")
	}
	if len(proxy) > 0 && proxy[0] != "" {
		res = append(res, proxy[0])
	}

	if len(res) == 0 {
		res = append(res, "127.0.0.1")
	}
	return
}

func (self *THandler) GetCookie(name, key string) (val string) {
	ck, err := self.Request.Cookie(name)
	if err != nil {
		return
	}

	val, _ = url.QueryUnescape(ck.Value)
	return
}

func (self *THandler) GetModulePath() string {
	return self.Route.FileName
}

// RemoteAddr returns more real IP address.
func (self *THandler) RemoteAddr() string {
	addr := self.Request.Header.Get("X-Real-IP")
	if len(addr) == 0 {
		addr = self.Request.Header.Get("X-Forwarded-For")
		if addr == "" {
			addr = self.Request.RemoteAddr
			if i := strings.LastIndex(addr, ":"); i > -1 {
				addr = addr[:i]
			}
		}
	}
	return addr
}

//SetCookie Sets the header entries associated with key to the single element value. It replaces any existing values associated with key.
//一个cookie  有名称,内容,原始值,域,大小,过期时间,安全
//cookie[0] => name string
//cookie[1] => value string
//cookie[2] => expires string
//cookie[3] => path string
//cookie[4] => domain string
func (self *THandler) SetCookie(name string, value string, others ...interface{}) {
	var b bytes.Buffer
	fmt.Fprintf(&b, "%s=%s", sanitizeCookieName(name), sanitizeCookieValue(value))

	if len(others) > 0 {
		switch others[0].(type) {
		case int:
			if others[0].(int) > 0 {
				fmt.Fprintf(&b, "; Max-Age=%d", others[0].(int))
			} else if others[0].(int) < 0 {
				fmt.Fprintf(&b, "; Max-Age=0")
			}
		case int64:
			if others[0].(int64) > 0 {
				fmt.Fprintf(&b, "; Max-Age=%d", others[0].(int64))
			} else if others[0].(int64) < 0 {
				fmt.Fprintf(&b, "; Max-Age=0")
			}
		case int32:
			if others[0].(int32) > 0 {
				fmt.Fprintf(&b, "; Max-Age=%d", others[0].(int32))
			} else if others[0].(int32) < 0 {
				fmt.Fprintf(&b, "; Max-Age=0")
			}
		}
	}
	if len(others) > 1 {
		fmt.Fprintf(&b, "; Path=%s", sanitizeCookieValue(others[1].(string)))
	}
	if len(others) > 2 {
		fmt.Fprintf(&b, "; Domain=%s", sanitizeCookieValue(others[2].(string)))
	}
	if len(others) > 3 {
		if others[3].(bool) {
			fmt.Fprintf(&b, "; Secure")
		}
	}

	if len(others) > 4 {
		if others[4].(bool) {
			fmt.Fprintf(&b, "; HttpOnly")
		}
	}
	self.Response.Header().Add("Set-Cookie", b.String())
	/*
		if aName == "" && aValue == "" { // 不能少于两个参数
			return
		}
		fmt.Println(args, len(args))
		var (
			//name    string
			//value   string
			expires int
			path    string
			domain  string
		)
		if len(args) > 0 {
			if v, ok := args[0].(int); ok {
				expires = v
			}
		}
		if len(args) > 1 {
			if v, ok := args[1].(string); ok {
				path = v
			}
		}
		if len(args) > 2 {
			if v, ok := args[2].(string); ok {
				domain = v
			}
		}

		lpCookie := &http.Cookie{
			Name:   aName,
			Value:  url.QueryEscape(aValue),
			Path:   path,
			Domain: domain,
		}

		if expires > 0 { //设置过期时间
			d, _ := time.ParseDuration(strconv.Itoa(expires) + "s")
			lpCookie.Expires = time.Now().Add(d)
		}
		if unique {
			self.Response.Header().Set("Set-Cookie", lpCookie.String()) // 等同http.SetCookie()

		} else {
			self.Response.Header().Add("Set-Cookie", lpCookie.String()) // 等同http.SetCookie()

		}
	*/
	/*
		if expires > 0 {
			p.COOKIE[pCookie.Name] = pCookie.Value
		} else {
			delete(p.COOKIE, pCookie.Name)
		}
	*/
}

// 添加Http头信息
func (self *THandler) SetHeader(unique bool, hdr string, val string) {
	if unique {
		self.Response.Header().Set(hdr, val)
	} else {
		self.Response.Header().Add(hdr, val)
	}
}

func (self *THandler) Abort(status int, body string) {
	self.Response.WriteHeader(status)
	self.Response.Write([]byte(body))
	//self.Result = body
}

func (self *THandler) RespondString(aBody string) {
	//self.Response.Write([]byte(aBody))
	self.Result = []byte(aBody)
}

func (self *THandler) Respond(aBody []byte) {
	self.Result = aBody
	//self.Response.Write(aBody)
}

func (self *THandler) RespondError(error string) {
	self.Header().Set("Content-Type", "text/plain; charset=utf-8")
	self.Header().Set("X-Content-Type-Options", "nosniff")
	self.WriteHeader(http.StatusInternalServerError)
	fmt.Fprintln(self, error)
}

func (self *THandler) NotModified() {
	self.IResponseWriter.WriteHeader(304)
}

// Respond content by Json mode
func (self *THandler) RespondByJson(aBody interface{}) {

	lJson, err := json.Marshal(aBody)
	if err != nil {
		self.Response.Write([]byte(err.Error()))
		return
	}

	self.Response.Header().Set("Content-Type", "application/json; charset=UTF-8")
	self.Result = lJson
}

// Ck
func (self *THandler) Redirect(urlStr string, status ...int) {
	//http.Redirect(self, self.Request, urlStr, code)
	lStatusCode := http.StatusFound
	if len(status) > 0 {
		lStatusCode = status[0]
	}

	self.Header().Set("Location", urlStr)
	self.WriteHeader(lStatusCode)
	//self.Write([]byte("Redirecting to: " + urlStr))
	self.Result = []byte("Redirecting to: " + urlStr)
}

func (self *THandler) Download(file_path string) error {
	f, err := os.Open(file_path)
	if err != nil {
		return err
	}
	defer f.Close()

	fName := filepath.Base(file_path)
	self.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%v\"", fName))
	_, err = io.Copy(self, f)
	return err
}

func (self *THandler) ServeFile(file_path string) {
	http.ServeFile(self.Response, self.Request, file_path)
}

// 废弃 Ck
// X+Org渲染 自动组合文件路径[根据Route的优先读取Module里Templete]
func (self *THandler) __RenderToResponse(aTemplateFile string, w http.ResponseWriter, context interface{}) {
	var lContext map[string]interface{}
	var ok bool
	if lContext, ok = context.(map[string]interface{}); ok {
		utils.MergeMaps(self.Router.GVar, lContext) // 添加Router的全局变量到Templete
	} else {
		lContext = self.Router.GVar // 添加Router的全局变量到Templete
	}

	if self.Route.FilePath == "" {
		err := self.Router.Template.RenderToWriter(filepath.Join(TEMPLATE_DIR, aTemplateFile), lContext, w)
		if err != nil {
			logger.Err(err.Error())
			//Trace("RenderToResponse:", filepath.Join(MODULE_DIR, self.Route.FilePath,TEMPLATE_DIR, aTemplateFile))
		}
	} else {
		err := self.Router.Template.RenderToWriter(filepath.Join(MODULE_DIR, self.Route.FilePath, TEMPLATE_DIR, aTemplateFile), lContext, w)
		if err != nil {
			logger.Err(err.Error())
			//Trace("RenderToResponse:", filepath.Join(MODULE_DIR, self.Route.FilePath,TEMPLATE_DIR, aTemplateFile))
		}
	}
}

func (self *THandler) RenderTemplate(aTemplateFile string, aArgs interface{}) {
	self.ContentType = "text/html; charset=utf-8"
	if a, ok := aArgs.(map[string]interface{}); ok {
		self.RenderArgs = utils.MergeMaps(self.Router.GVar, a) // 添加Router的全局变量到Templete
	} else {
		self.RenderArgs = self.Router.GVar // 添加Router的全局变量到Templete
	}

	if self.Route.FilePath == "" {
		self.TemplateSrc = filepath.Join(TEMPLATE_DIR, aTemplateFile)
	} else {
		self.TemplateSrc = filepath.Join(MODULE_DIR, self.Route.FilePath, TEMPLATE_DIR, aTemplateFile)
		//self.TemplateSrc = filepath.Join(self.Route.FilePath,TEMPLATE_DIR, aTemplateFile)

	}
	logger.Info("RenderTemplate", self.Route.FilePath, self.TemplateSrc)
}

// Responds with 404 Not Found
func (self *THandler) RespondWithNotFound(message ...string) {
	//self.Abort(http.StatusNotFound, body)
	if len(message) == 0 {
		self.Abort(http.StatusNotFound, http.StatusText(http.StatusNotFound))
		return
	}
	self.Abort(http.StatusNotFound, message[0])
}

// Responds with 404 Not Found
func (self *THandler) RespondWithNotFoundPage(HtmlFile string) {
	//self.Router.RenderTemplate(TEMPLATES_ROOT+"/"+HtmlFile, self, nil)
	self.Router.Server.Template.RenderToWriter(filepath.Join(MODULE_DIR, self.Route.Path, TEMPLATE_DIR, HtmlFile), nil, self)
}

// Checks whether the HTTP method is GET or not
func (self *THandler) IsGet() bool {
	return self.Request.Method == "GET"
}

// Checks whether the HTTP method is POST or not
func (self *THandler) IsPost() bool {
	return self.Request.Method == "POST"
}

// Checks whether the HTTP method is PUT or not
func (self *THandler) IsPut() bool {
	return self.Request.Method == "PUT"
}

// Checks whether the HTTP method is DELETE or not
func (self *THandler) IsDelete() bool {
	return self.Request.Method == "DELETE"
}

func singleJoiningSlash(a, b string) string {
	aslash := strings.HasSuffix(a, "/")
	bslash := strings.HasPrefix(b, "/")
	switch {
	case aslash && bslash:
		return a + b[1:]
	case !aslash && !bslash:
		return a + "/" + b
	}
	return a + b
}

func (self *TProxyHandler) connect(rw IResponseWriter, req *http.Request, Router *TRouter, Route *TRoute) {
	self.Request = req
	self.Response = rw
	self.IResponseWriter = rw
	self.Router = Router
	self.Route = Route
	//self.Logger = Router.Server.Logger

	director := func(req *http.Request) {
		target := self.Route.Host
		targetQuery := target.RawQuery
		req.URL.Scheme = self.Route.Host.Scheme
		req.URL.Host = self.Route.Host.Host
		req.URL.Path = singleJoiningSlash(target.Path, req.URL.Path)
		if targetQuery == "" || req.URL.RawQuery == "" {
			req.URL.RawQuery = targetQuery + req.URL.RawQuery
		} else {
			req.URL.RawQuery = targetQuery + "&" + req.URL.RawQuery
		}
		if _, ok := req.Header["User-Agent"]; !ok {
			// explicitly disable User-Agent so it's not set to default value
			req.Header.Set("User-Agent", "")
		}
	}

	if Route.Host.Scheme == "http" {
		self.Director = director
		self.Transport = http.DefaultTransport

	} else {
		self.Director = func(req *http.Request) {
			director(req)
			req.Host = req.URL.Host
		}

		// Set a custom DialTLS to access the TLS connection state
		self.Transport = &http.Transport{
			DialTLS: func(network, addr string) (net.Conn, error) {
				conn, err := net.Dial(network, addr)
				if err != nil {
					return nil, err
				}

				host, _, err := net.SplitHostPort(addr)
				if err != nil {
					return nil, err
				}
				cfg := &tls.Config{ServerName: host}

				tlsConn := tls.Client(conn, cfg)
				if err := tlsConn.Handshake(); err != nil {
					conn.Close()
					return nil, err
				}

				cs := tlsConn.ConnectionState()
				cert := cs.PeerCertificates[0]

				// Verify here
				cert.VerifyHostname(host)
				//self.Logger.Dbg(cert.Subject)

				return tlsConn, nil
			}}
	}
}

func (p *TProxyHandler) copyBuffer(dst io.Writer, src io.Reader, buf []byte) (int64, error) {
	if len(buf) == 0 {
		buf = make([]byte, 32*1024)
	}
	var written int64
	for {
		nr, rerr := src.Read(buf)
		if rerr != nil && rerr != io.EOF {
			logger.Err("httputil: ReverseProxy read error during body copy: %v", rerr)
		}
		if nr > 0 {
			nw, werr := dst.Write(buf[:nr])
			if nw > 0 {
				written += int64(nw)
			}
			if werr != nil {
				return written, werr
			}
			if nr != nw {
				return written, io.ErrShortWrite
			}
		}
		if rerr != nil {
			return written, rerr
		}
	}
}

func (p *TProxyHandler) copyResponse(dst io.Writer, src io.Reader) {
	if p.FlushInterval != 0 {
		if wf, ok := dst.(writeFlusher); ok {
			mlw := &maxLatencyWriter{
				dst:     wf,
				latency: p.FlushInterval,
				done:    make(chan bool),
			}
			go mlw.flushLoop()
			defer mlw.stop()
			dst = mlw
		}
	}

	var buf []byte
	if p.BufferPool != nil {
		buf = p.BufferPool.Get()
	}
	p.copyBuffer(dst, src, buf)
	if p.BufferPool != nil {
		p.BufferPool.Put(buf)
	}
}

/*
func (self *TRestHandle) RespondByJson(src interface{}) {
	//var s []map[string]string
	//rawValue := reflect.Indirect(reflect.ValueOf(src))
	//s = rawValue.Interface().([]map[string]string)
	runtime.ReadMemStats(memstats)
	fmt.Printf("MemStats=%d|%d|%d|%d|RespondByJson", memstats.Mallocs, memstats.Sys, memstats.Frees, memstats.HeapObjects)

	s1, _ := json.Marshal(src)
	//str := string(s)
	//fmt.Println(s1)
	runtime.ReadMemStats(memstats)
	fmt.Printf("MemStats=%d|%d|%d|%d|RespondByJson", memstats.Mallocs, memstats.Sys, memstats.Frees, memstats.HeapObjects)

	self.Write(s1)
	runtime.ReadMemStats(memstats)
	fmt.Printf("MemStats=%d|%d|%d|%d|RespondByJson", memstats.Mallocs, memstats.Sys, memstats.Frees, memstats.HeapObjects)

}
*/
func sanitizeCookieName(n string) string {
	return cookieNameSanitizer.Replace(n)
}

func sanitizeCookieValue(v string) string {
	return cookieValueSanitizer.Replace(v)
	//return sanitizeOrWarn("Cookie.Value", validCookieValueByte, v)
}

func sanitizeOrWarn(fieldName string, valid func(byte) bool, v string) string {
	ok := true
	for i := 0; i < len(v); i++ {
		if valid(v[i]) {
			continue
		}
		fmt.Printf("net/http: invalid byte %q in %s; dropping invalid bytes", v[i], fieldName)
		ok = false
		break
	}
	if ok {
		return v
	}
	buf := make([]byte, 0, len(v))
	for i := 0; i < len(v); i++ {
		if b := v[i]; valid(b) {
			buf = append(buf, b)
		}
	}
	return string(buf)
}

func validCookieValueByte(b byte) bool {
	return 0x20 < b && b < 0x7f && b != '"' && b != ',' && b != ';' && b != '\\'
}

/*
func init() {
	CookieSessions, _ = xsession.NewManager("cookie", `{"cookieName":"IV_C","enableSetCookie":true,"gclifetime":3600,"maxLifetime":86400,"ProviderConfig":"{\"cookieName\":\"agosessionid\",\"securityKey\":\"beegocookiehashkey\"}"}`)
	go CookieSessions.GC()
	MemorySessions, _ = xsession.NewManager("memory", `{"cookieName":"IV_M","gclifetime":3600,"maxLifetime":172800}`)
	go MemorySessions.GC()
}
*/

func (m *maxLatencyWriter) Write(p []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.dst.Write(p)
}

func (m *maxLatencyWriter) flushLoop() {
	t := time.NewTicker(m.latency)
	defer t.Stop()
	for {
		select {
		case <-m.done:
			if onExitFlushLoop != nil {
				onExitFlushLoop()
			}
			return
		case <-t.C:
			m.mu.Lock()
			m.dst.Flush()
			m.mu.Unlock()
		}
	}
}

func (m *maxLatencyWriter) stop() { m.done <- true }
