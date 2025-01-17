package xhr

import (
	"bytes"
	"github.com/GopeedLab/gopeed/pkg/download/engine/inject"
	"github.com/GopeedLab/gopeed/pkg/download/engine/inject/file"
	"github.com/GopeedLab/gopeed/pkg/download/engine/inject/formdata"
	"github.com/dop251/goja"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"time"
)

const (
	eventLoad             = "load"
	eventReadystatechange = "readystatechange"
	eventProgress         = "progress"
	eventAbort            = "abort"
	eventError            = "error"
	eventTimeout          = "timeout"
)

type ProgressEvent struct {
	Type             string `json:"type"`
	LengthComputable bool   `json:"lengthComputable"`
	Loaded           int64  `json:"loaded"`
	Total            int64  `json:"total"`
}

type EventProp struct {
	eventListeners map[string]func(event *ProgressEvent)
	Onload         func(event *ProgressEvent) `json:"onload"`
	Onprogress     func(event *ProgressEvent) `json:"onprogress"`
	Onabort        func(event *ProgressEvent) `json:"onabort"`
	Onerror        func(event *ProgressEvent) `json:"onerror"`
	Ontimeout      func(event *ProgressEvent) `json:"ontimeout"`
}

func (ep *EventProp) AddEventListener(event string, cb func(event *ProgressEvent)) {
	ep.eventListeners[event] = cb
}

func (ep *EventProp) RemoveEventListener(event string) {
	delete(ep.eventListeners, event)
}

func (ep *EventProp) callOnload() {
	event := &ProgressEvent{
		Type:             eventLoad,
		LengthComputable: false,
	}
	if ep.Onload != nil {
		ep.Onload(event)
	}
	ep.callEventListener(event)
}

func (ep *EventProp) callOnprogress(loaded, total int64) {
	event := &ProgressEvent{
		Type:             eventProgress,
		LengthComputable: true,
		Loaded:           loaded,
		Total:            total,
	}
	if ep.Onprogress != nil {
		ep.Onprogress(event)
	}
	ep.callEventListener(event)
}

func (ep *EventProp) callOnabort() {
	event := &ProgressEvent{
		Type:             eventAbort,
		LengthComputable: false,
	}
	if ep.Onabort != nil {
		ep.Onabort(event)
	}
	ep.callEventListener(event)
}

func (ep *EventProp) callOnerror() {
	event := &ProgressEvent{
		Type:             eventError,
		LengthComputable: false,
	}
	if ep.Onerror != nil {
		ep.Onerror(event)
	}
	ep.callEventListener(event)
}

func (ep *EventProp) callOntimeout() {
	event := &ProgressEvent{
		Type:             eventTimeout,
		LengthComputable: false,
	}
	if ep.Ontimeout != nil {
		ep.Ontimeout(event)
	}
	ep.callEventListener(event)
}

func (ep *EventProp) callEventListener(event *ProgressEvent) {
	if cb, ok := ep.eventListeners[event.Type]; ok {
		cb(event)
	}
}

type XMLHttpRequestUpload struct {
	*EventProp
}

type XMLHttpRequest struct {
	method          string
	url             string
	requestHeaders  map[string]string
	responseHeaders map[string]string
	aborted         bool
	proxyUrl        *url.URL

	Upload       *XMLHttpRequestUpload `json:"upload"`
	Timeout      int                   `json:"timeout"`
	ReadyState   int                   `json:"readyState"`
	Status       int                   `json:"status"`
	StatusText   string                `json:"statusText"`
	Response     string                `json:"response"`
	ResponseText string                `json:"responseText"`
	*EventProp
	Onreadystatechange func(event *ProgressEvent) `json:"onreadystatechange"`
}

func (xhr *XMLHttpRequest) Open(method, url string) {
	xhr.method = method
	xhr.url = url
	xhr.requestHeaders = make(map[string]string)
	xhr.responseHeaders = make(map[string]string)
	xhr.doReadystatechange(1)
}

func (xhr *XMLHttpRequest) SetRequestHeader(key, value string) {
	xhr.requestHeaders[key] = value
}

func (xhr *XMLHttpRequest) Send(data goja.Value) {
	var req *http.Request
	var err error
	d := xhr.parseData(data)
	var (
		contentType   string
		contentLength int64
	)
	if d == nil || xhr.method == "GET" || xhr.method == "HEAD" {
		req, err = http.NewRequest(xhr.method, xhr.url, nil)
	} else {
		switch d.(type) {
		case string:
			req, err = http.NewRequest(xhr.method, xhr.url, bytes.NewBufferString(d.(string)))
			contentType = "text/plain;charset=UTF-8"
			contentLength = int64(len(d.(string)))
		case *file.File:
			req, err = http.NewRequest(xhr.method, xhr.url, d.(*file.File).Reader)
			contentType = "application/octet-stream"
			contentLength = d.(*file.File).Size
		case *formdata.FormData:
			pr, pw := io.Pipe()
			mw := NewMultipart(pw)
			for _, e := range d.(*formdata.FormData).Entries() {
				arr := e.([]any)
				k := arr[0].(string)
				v := arr[1]
				switch v.(type) {
				case string:
					mw.WriteField(k, v.(string))
				case *file.File:
					mw.WriteFile(k, v.(*file.File))
				}
			}
			go func() {
				defer pw.Close()
				defer mw.Close()
				mw.Send()
			}()
			req, err = http.NewRequest(xhr.method, xhr.url, pr)
			contentType = mw.FormDataContentType()
			contentLength = mw.Size()
		}
	}
	if err != nil {
		xhr.callOnerror()
		return
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if contentLength > 0 {
		req.ContentLength = contentLength
	}
	for k, v := range xhr.requestHeaders {
		req.Header.Set(k, v)
	}
	transport := &http.Transport{}
	if xhr.proxyUrl != nil {
		transport.Proxy = http.ProxyURL(xhr.proxyUrl)
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   time.Duration(xhr.Timeout) * time.Millisecond,
	}
	resp, err := client.Do(req)
	if err != nil {
		// handle timeout error
		if err, ok := err.(net.Error); ok && err.Timeout() {
			if xhr.Timeout > 0 {
				xhr.Upload.callOntimeout()
				xhr.callOntimeout()
			}
			return
		}
		xhr.Upload.callOnerror()
		xhr.callOnerror()
		return
	}
	defer resp.Body.Close()
	xhr.Upload.callOnprogress(contentLength, contentLength)
	if !xhr.aborted {
		xhr.Upload.callOnload()
	}
	for k, v := range resp.Header {
		xhr.responseHeaders[k] = v[0]
	}
	xhr.Status = resp.StatusCode
	xhr.StatusText = resp.Status
	xhr.doReadystatechange(2)
	buf, err := io.ReadAll(resp.Body)
	if err != nil {
		xhr.callOnerror()
		return
	}
	xhr.doReadystatechange(3)
	xhr.Response = string(buf)
	xhr.ResponseText = xhr.Response
	xhr.doReadystatechange(4)
	respBodyLen := int64(len(buf))
	xhr.callOnprogress(respBodyLen, respBodyLen)
	if !xhr.aborted {
		xhr.callOnload()
	}
	return
}

func (xhr *XMLHttpRequest) Abort() {
	xhr.doReadystatechange(0)
	xhr.aborted = true
	xhr.Upload.callOnabort()
	xhr.callOnabort()
}

func (xhr *XMLHttpRequest) GetResponseHeader(key string) string {
	return xhr.responseHeaders[key]
}

func (xhr *XMLHttpRequest) GetAllResponseHeaders() string {
	var buf bytes.Buffer
	for k, v := range xhr.responseHeaders {
		buf.WriteString(k)
		buf.WriteString(": ")
		buf.WriteString(v)
		buf.WriteString("\r\n")
	}
	return buf.String()
}

func (xhr *XMLHttpRequest) callOnreadystatechange() {
	event := &ProgressEvent{
		Type:             eventReadystatechange,
		LengthComputable: false,
	}
	if xhr.Onreadystatechange != nil {
		xhr.Onreadystatechange(event)
	}
	xhr.callEventListener(event)
}

func (xhr *XMLHttpRequest) doReadystatechange(state int) {
	if xhr.aborted {
		return
	}
	xhr.ReadyState = state
	xhr.callOnreadystatechange()
}

// parse js data to go struct
func (xhr *XMLHttpRequest) parseData(data goja.Value) any {
	// check if data is null or undefined
	if data == nil || goja.IsNull(data) || goja.IsUndefined(data) || goja.IsNaN(data) {
		return nil
	}
	// check if data is File
	f, ok := data.Export().(*file.File)
	if ok {
		return f
	}
	// check if data is FormData
	fd, ok := data.Export().(*formdata.FormData)
	if ok {
		return fd
	}
	// otherwise, return data as string
	return data.String()
}

func Enable(runtime *goja.Runtime, proxyUrl *url.URL) error {
	progressEvent := runtime.ToValue(func(call goja.ConstructorCall) *goja.Object {
		if len(call.Arguments) < 1 {
			inject.ThrowTypeError(runtime, "Failed to construct 'ProgressEvent': 1 argument required, but only 0 present.")
		}
		instance := &ProgressEvent{
			Type: call.Argument(0).String(),
		}
		instanceValue := runtime.ToValue(instance).(*goja.Object)
		instanceValue.SetPrototype(call.This.Prototype())
		return instanceValue
	})
	xhr := runtime.ToValue(func(call goja.ConstructorCall) *goja.Object {
		instance := &XMLHttpRequest{
			proxyUrl: proxyUrl,
			Upload: &XMLHttpRequestUpload{
				EventProp: &EventProp{
					eventListeners: make(map[string]func(event *ProgressEvent)),
				},
			},
			EventProp: &EventProp{
				eventListeners: make(map[string]func(event *ProgressEvent)),
			},
		}
		instanceValue := runtime.ToValue(instance).(*goja.Object)
		instanceValue.SetPrototype(call.This.Prototype())
		return instanceValue
	})
	if err := runtime.Set("ProgressEvent", progressEvent); err != nil {
		return err
	}
	if err := runtime.Set("XMLHttpRequest", xhr); err != nil {
		return err
	}
	return nil
}

// Wrap multipart.Writer and stat content length
type multipartWrapper struct {
	statBuffer *bytes.Buffer
	statWriter *multipart.Writer
	writer     *multipart.Writer
	fields     map[string]any
}

func NewMultipart(w io.Writer) *multipartWrapper {
	var buf bytes.Buffer
	return &multipartWrapper{
		statBuffer: &buf,
		statWriter: multipart.NewWriter(&buf),
		writer:     multipart.NewWriter(w),
		fields:     make(map[string]any),
	}
}

func (w *multipartWrapper) WriteField(fieldname string, value string) error {
	w.fields[fieldname] = value
	return w.statWriter.WriteField(fieldname, value)
}

func (w *multipartWrapper) WriteFile(fieldname string, file *file.File) error {
	w.fields[fieldname] = file
	_, err := w.statWriter.CreateFormFile(fieldname, file.Name)
	if err != nil {
		return err
	}
	return nil
}

func (w *multipartWrapper) Size() int64 {
	w.statWriter.Close()
	size := int64(w.statBuffer.Len())
	for _, v := range w.fields {
		switch v.(type) {
		case *file.File:
			f := v.(*file.File)
			size += f.Size
		}
	}
	return size
}

func (w *multipartWrapper) Send() error {
	for k, v := range w.fields {
		switch v.(type) {
		case string:
			if err := w.writer.WriteField(k, v.(string)); err != nil {
				return err
			}
		case *file.File:
			f := v.(*file.File)
			fw, err := w.writer.CreateFormFile(k, f.Name)
			if err != nil {
				return err
			}
			if _, err = io.Copy(fw, f); err != nil {
				return err
			}
		}
	}
	return nil
}

func (w *multipartWrapper) FormDataContentType() string {
	return w.writer.FormDataContentType()
}

func (w *multipartWrapper) Close() error {
	return w.writer.Close()
}
