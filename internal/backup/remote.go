package backup

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type Destination struct {
	Provider        string `json:"provider"`
	Endpoint        string `json:"endpoint"`
	Prefix          string `json:"prefix"`
	Username        string `json:"username"`
	Password        string `json:"password,omitempty"`
	Region          string `json:"region"`
	Bucket          string `json:"bucket"`
	AccessKeyID     string `json:"accessKeyId"`
	SecretAccessKey string `json:"secretAccessKey,omitempty"`
	PathStyle       bool   `json:"pathStyle"`
}

type Object struct {
	Name       string    `json:"name"`
	Size       int64     `json:"size"`
	ModifiedAt time.Time `json:"modifiedAt"`
}

var archiveName = regexp.MustCompile(`^readest-[0-9]{8}T[0-9]{6}Z-[a-f0-9]{32}\.tar\.gz\.age$`)

func ValidName(name string) bool { return archiveName.MatchString(name) }

func (d *Destination) Validate() error {
	if d.Provider != "webdav" && d.Provider != "s3" {
		return errors.New("请选择 WebDAV 或 S3")
	}
	d.Prefix = strings.Trim(d.Prefix, "/")
	if d.Prefix == "" {
		d.Prefix = "readest-backups"
	}
	if !fs.ValidPath(d.Prefix) || strings.ContainsAny(d.Prefix, "\\:%?#\x00") || len(d.Prefix) > 512 {
		return errors.New("备份目录必须为相对路径，不能包含 .. 或 URL 转义字符")
	}
	if d.Endpoint != "" {
		u, err := url.Parse(d.Endpoint)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return errors.New("服务器地址必须为不带凭据、查询参数或片段的 HTTP(S) URL")
		}
	}
	if len(d.Endpoint)+len(d.Username)+len(d.Password)+len(d.AccessKeyID)+len(d.SecretAccessKey)+len(d.Region)+len(d.Bucket) > 16*1024 {
		return errors.New("备份配置过长")
	}
	if d.Provider == "webdav" && d.Endpoint == "" {
		return errors.New("请填写 WebDAV 地址")
	}
	if d.Provider == "s3" && (d.Bucket == "" || d.Region == "" || d.AccessKeyID == "" || d.SecretAccessKey == "") {
		return errors.New("请填写 S3 区域、存储桶及访问密钥")
	}
	return nil
}

type Remote struct {
	destination Destination
	http        *http.Client
	s3          *s3.Client
}

func NewRemote(d Destination) (*Remote, error) {
	if err := d.Validate(); err != nil {
		return nil, err
	}
	client := &http.Client{
		Transport:     &http.Transport{Proxy: http.ProxyFromEnvironment, ResponseHeaderTimeout: 30 * time.Second, TLSHandshakeTimeout: 10 * time.Second},
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}
	r := &Remote{destination: d, http: client}
	if d.Provider == "s3" {
		options := s3.Options{
			Region: d.Region, Credentials: credentials.NewStaticCredentialsProvider(d.AccessKeyID, d.SecretAccessKey, ""),
			HTTPClient: client, UsePathStyle: d.PathStyle,
			RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
			ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
		}
		if d.Endpoint != "" {
			options.BaseEndpoint = aws.String(d.Endpoint)
		}
		r.s3 = s3.New(options)
	}
	return r, nil
}

func (r *Remote) Close() { r.http.CloseIdleConnections() }

func (r *Remote) key(name string) string { return r.destination.Prefix + "/" + name }

func (r *Remote) davURL(relative string) string {
	u, _ := url.JoinPath(r.destination.Endpoint+"/", relative)
	return u
}

func (r *Remote) dav(ctx context.Context, method, relative string, body io.Reader, size int64) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, r.davURL(relative), body)
	if err != nil {
		return nil, errors.New("无法创建 WebDAV 请求")
	}
	if body != nil {
		req.ContentLength = size
	}
	if r.destination.Username != "" || r.destination.Password != "" {
		req.SetBasicAuth(r.destination.Username, r.destination.Password)
	}
	if method == "PROPFIND" {
		req.Header.Set("Depth", "1")
		req.Header.Set("Content-Type", "application/xml")
	}
	if method == "MOVE" {
		req.Header.Set("Destination", r.davURL(strings.TrimSuffix(relative, ".uploading")))
		req.Header.Set("Overwrite", "F")
	}
	res, err := r.http.Do(req)
	if err != nil {
		return nil, errors.New("WebDAV 连接失败，请检查地址、证书和网络")
	}
	return res, nil
}

func remoteStatus(provider string, status int) error {
	return fmt.Errorf("%s 请求失败（HTTP %d），请检查地址和访问权限", provider, status)
}

func (r *Remote) ensureDAV(ctx context.Context) error {
	parts := strings.Split(r.destination.Prefix, "/")
	for i := range parts {
		res, err := r.dav(ctx, "MKCOL", strings.Join(parts[:i+1], "/"), nil, 0)
		if err != nil {
			return err
		}
		_ = res.Body.Close()
		if res.StatusCode != 201 && res.StatusCode != 405 {
			return remoteStatus("WebDAV", res.StatusCode)
		}
	}
	return nil
}

func (r *Remote) Upload(ctx context.Context, name string, file *os.File) error {
	if !ValidName(name) {
		return errors.New("无效的备份名称")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if r.s3 != nil {
		_, err := transfermanager.New(r.s3, func(o *transfermanager.Options) {
			o.Concurrency = 2
			o.PartSizeBytes = 16 << 20
			o.FailTimeout = 15 * time.Second
			o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
		}).UploadObject(ctx, &transfermanager.UploadObjectInput{
			Bucket: aws.String(r.destination.Bucket), Key: aws.String(r.key(name)), Body: file,
			ContentType: aws.String("application/octet-stream"),
		})
		if err != nil {
			return errors.New("S3 上传失败，请检查地址、存储桶、访问密钥和网络")
		}
		return nil
	}
	if err := r.ensureDAV(ctx); err != nil {
		return err
	}
	stat, err := file.Stat()
	if err != nil {
		return err
	}
	temporary := r.key(name) + ".uploading"
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if res, err := r.dav(cleanup, "DELETE", temporary, nil, 0); err == nil {
			_ = res.Body.Close()
		}
	}()
	res, err := r.dav(ctx, "PUT", temporary, file, stat.Size())
	if err != nil {
		return err
	}
	_ = res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return remoteStatus("WebDAV", res.StatusCode)
	}
	res, err = r.dav(ctx, "MOVE", temporary, nil, 0)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return remoteStatus("WebDAV MOVE", res.StatusCode)
	}
	return nil
}

func (r *Remote) Download(ctx context.Context, name string, dst io.Writer, limit int64) error {
	if !ValidName(name) {
		return errors.New("无效的备份名称")
	}
	var body io.ReadCloser
	if r.s3 != nil {
		res, err := r.s3.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(r.destination.Bucket), Key: aws.String(r.key(name))})
		if err != nil {
			return errors.New("无法下载 S3 备份，请检查对象名称和访问权限")
		}
		body = res.Body
	} else {
		res, err := r.dav(ctx, "GET", r.key(name), nil, 0)
		if err != nil {
			return err
		}
		if res.StatusCode != 200 {
			_ = res.Body.Close()
			return remoteStatus("WebDAV", res.StatusCode)
		}
		body = res.Body
	}
	defer body.Close()
	n, err := io.Copy(dst, io.LimitReader(body, limit+1))
	if err != nil {
		return errors.New("下载备份失败")
	}
	if n > limit {
		return errors.New("备份超过服务器的大小限制")
	}
	return nil
}

func (r *Remote) List(ctx context.Context) ([]Object, error) {
	objects := make([]Object, 0)
	if r.s3 != nil {
		pager := s3.NewListObjectsV2Paginator(r.s3, &s3.ListObjectsV2Input{Bucket: aws.String(r.destination.Bucket), Prefix: aws.String(r.destination.Prefix + "/")})
		for pager.HasMorePages() {
			page, err := pager.NextPage(ctx)
			if err != nil {
				return nil, errors.New("无法列出 S3 备份，请检查存储桶和访问密钥")
			}
			for _, obj := range page.Contents {
				name := strings.TrimPrefix(aws.ToString(obj.Key), r.destination.Prefix+"/")
				if ValidName(name) {
					objects = append(objects, Object{Name: name, Size: aws.ToInt64(obj.Size), ModifiedAt: aws.ToTime(obj.LastModified)})
				}
			}
			if len(objects) > 10000 {
				return nil, errors.New("备份数量超过 10000，请缩小备份目录范围")
			}
		}
	} else {
		payload := `<d:propfind xmlns:d="DAV:"><d:prop><d:getcontentlength/><d:getlastmodified/><d:resourcetype/></d:prop></d:propfind>`
		res, err := r.dav(ctx, "PROPFIND", r.destination.Prefix+"/", strings.NewReader(payload), int64(len(payload)))
		if err != nil {
			return nil, err
		}
		defer res.Body.Close()
		if res.StatusCode == 404 {
			return objects, nil
		}
		if res.StatusCode != 207 {
			return nil, remoteStatus("WebDAV", res.StatusCode)
		}
		var listing struct {
			Responses []struct {
				Href       string `xml:"href"`
				Properties []struct {
					Status string `xml:"status"`
					Prop   struct {
						Size     string `xml:"getcontentlength"`
						Modified string `xml:"getlastmodified"`
					} `xml:"prop"`
				} `xml:"propstat"`
			} `xml:"response"`
		}
		if err := xml.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(&listing); err != nil {
			return nil, errors.New("WebDAV 目录响应无效或过大")
		}
		base, _ := url.Parse(r.davURL(r.destination.Prefix + "/"))
		for _, obj := range listing.Responses {
			href, err := url.Parse(obj.Href)
			if err != nil {
				continue
			}
			href = base.ResolveReference(href)
			name := path.Base(href.Path)
			expected, _ := url.Parse(r.davURL(r.key(name)))
			if !ValidName(name) || href.Scheme != expected.Scheme || href.Host != expected.Host || href.Path != expected.Path {
				continue
			}
			for _, prop := range obj.Properties {
				if !strings.Contains(prop.Status, " 200 ") {
					continue
				}
				size, _ := strconv.ParseInt(prop.Prop.Size, 10, 64)
				modified, _ := http.ParseTime(prop.Prop.Modified)
				objects = append(objects, Object{Name: name, Size: size, ModifiedAt: modified})
				break
			}
		}
	}
	sort.Slice(objects, func(i, j int) bool { return objects[i].Name > objects[j].Name })
	return objects, nil
}
