package chrome_test

import (
	"testing"

	"github.com/grngxd/majorca/browser/chrome"
)

func TestCreateChrome(t *testing.T) {
	c, err := chrome.New()
	if err != nil {
		t.Fatalf("Failed to create Chrome browser: %v", err)
	}

	// defer func() {
	// 	if err := c.Kill(); err != nil {
	// 		t.Errorf("Failed to kill Chrome process: %v", err)
	// 	}
	// }()

	t.Log("Chrome browser created")
	t.Logf("Path: %s", c.Path)

	err = c.Load("https://new.grng.cc")
	if err != nil {
		t.Fatalf("Failed to load URL: %v", err)
	}

	// Evaluate JavaScript to get the document title
	value, typ, err := c.Eval("document.title")
	if err != nil {
		t.Fatalf("Failed to evaluate JavaScript: %v", err)
	}

	t.Logf("Page title: %s, Type: %s", value, typ)
}
