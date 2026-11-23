package api

import (
	"archive/zip"
	"reflect"
	"strings"
)

// shellEscape escapes a string for safe use inside a double-quoted bash argument.
// It escapes characters that have special meaning in double-quoted strings: \, $, `, ".
func shellEscape(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "`", "\\`", `$`, `\$`)
	return r.Replace(s)
}

// hclEscape escapes a string for use inside a double-quoted HCL string. HCL
// also starts an interpolation at "${" and a template directive at "%{", so
// both are doubled into their literal "$${" and "%%{" forms (#952). Line
// breaks are not escaped: renderTemplate refuses any value holding one
// (checkRenderDataSingleLine, #970).
func hclEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "${", "$${", "%{", "%%{").Replace(s)
}

// hclEscapeData returns a copy of data with every string field HCL-escaped,
// for rendering the bundle's tfvars. Most fields are server-derived, but
// ContactEmail is the session user's email, which only has to pass
// mail.ParseAddress and so can carry quotes and "${" in a quoted local part
// (#952). It walks the struct by reflection so a new field cannot be missed.
func hclEscapeData(data federationIaCData) federationIaCData {
	v := reflect.ValueOf(&data).Elem()
	for i := 0; i < v.NumField(); i++ {
		if f := v.Field(i); f.Kind() == reflect.String {
			f.SetString(hclEscape(f.String()))
		}
	}
	return data
}

// ---------------------------------------------------------------------------
// Zip helpers
// ---------------------------------------------------------------------------

func addBytesToZip(zw *zip.Writer, name string, content []byte) error {
	f, err := zw.Create(name)
	if err != nil {
		return err
	}
	_, err = f.Write(content)
	return err
}

// addExecBytesToZip writes content to the zip with Unix mode 0755 so that
// customers can run the resulting shell script without an explicit chmod.
func addExecBytesToZip(zw *zip.Writer, name string, content []byte) error {
	hdr := &zip.FileHeader{
		Name:   name,
		Method: zip.Deflate,
	}
	hdr.SetMode(0755)
	f, err := zw.CreateHeader(hdr)
	if err != nil {
		return err
	}
	_, err = f.Write(content)
	return err
}

func addStringToZip(zw *zip.Writer, name, content string) error {
	return addBytesToZip(zw, name, []byte(content))
}
