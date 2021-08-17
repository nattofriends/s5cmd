package e2e

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/fs"
	"gotest.tools/v3/icmd"
)

// sync folder/ folder2/
func TestSyncLocalToLocalNotPermitted(t *testing.T) {
	t.Parallel()

	_, s5cmd, cleanup := setup(t)
	defer cleanup()

	const (
		sourceFolder = "source"
		destFolder   = "dest"
	)
	sourceWorkDir := fs.NewDir(t, sourceFolder)
	destWorkDir := fs.NewDir(t, destFolder)

	srcpath := filepath.ToSlash(sourceWorkDir.Path())
	destpath := filepath.ToSlash(destWorkDir.Path())

	cmd := s5cmd("sync", srcpath, destpath)
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Expected{ExitCode: 1})

	assertLines(t, result.Stderr(), map[int]compareFunc{
		0: equals(`ERROR "sync %s %s": local->local sync operations are not permitted`, srcpath, destpath),
	})
}

// sync source.go s3://buckey
func TestSyncLocalFileToS3NotPermitted(t *testing.T) {
	t.Parallel()

	s3client, s5cmd, cleanup := setup(t)
	defer cleanup()

	const (
		filename = "source.go"
		bucket   = "bucket"
	)

	createBucket(t, s3client, bucket)

	sourceWorkDir := fs.NewFile(t, filename)
	srcpath := filepath.ToSlash(sourceWorkDir.Path())
	dstpath := fmt.Sprintf("s3://%s/", bucket)

	cmd := s5cmd("sync", srcpath, dstpath)
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Expected{ExitCode: 1})

	assertLines(t, result.Stderr(), map[int]compareFunc{
		0: equals(`ERROR "sync %s %s": local source must be a directory`, srcpath, dstpath),
	})
}

// sync s3://bucket/source.go .
func TestSyncSingleS3ObjectToLocalNotPermitted(t *testing.T) {
	t.Parallel()

	s3client, s5cmd, cleanup := setup(t)
	defer cleanup()

	const (
		filename = "source.go"
		bucket   = "bucket"
		content  = "content"
	)

	createBucket(t, s3client, bucket)
	putFile(t, s3client, bucket, filename, content)

	srcpath := fmt.Sprintf("s3://%s/%s", bucket, filename)

	cmd := s5cmd("sync", srcpath, ".")
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Expected{ExitCode: 1})

	assertLines(t, result.Stderr(), map[int]compareFunc{
		0: equals(`ERROR "sync %s .": remote source %q must be a bucket or a prefix`, srcpath, srcpath),
	})
}

// sync folder/ s3://bucket
func TestSyncLocalFolderToS3EmptyBucket(t *testing.T) {
	t.Parallel()
	s3client, s5cmd, cleanup := setup(t)
	defer cleanup()

	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	folderLayout := []fs.PathOp{
		fs.WithFile("testfile1.txt", "this is a test file 1"),
		fs.WithFile("readme.md", "this is a readme file"),
		fs.WithDir(
			"a",
			fs.WithFile("another_test_file.txt", "yet another txt file. yatf."),
		),
		fs.WithDir(
			"b",
			fs.WithFile("filename-with-hypen.gz", "file has hypen in its name"),
		),
	}

	workdir := fs.NewDir(t, "somedir", folderLayout...)
	defer workdir.Remove()

	src := fmt.Sprintf("%v/", workdir.Path())
	src = filepath.ToSlash(src)
	dst := fmt.Sprintf("s3://%v/", bucket)

	cmd := s5cmd("sync", src, dst)
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals(`upload %va/another_test_file.txt %va/another_test_file.txt`, src, dst),
		1: equals(`upload %vb/filename-with-hypen.gz %vb/filename-with-hypen.gz`, src, dst),
		2: equals(`upload %vreadme.md %vreadme.md`, src, dst),
		3: equals(`upload %vtestfile1.txt %vtestfile1.txt`, src, dst),
	}, sortInput(true))

	// assert local filesystem
	expected := fs.Expected(t, folderLayout...)
	assert.Assert(t, fs.Equal(workdir.Path(), expected))

	expectedS3Content := map[string]string{
		"testfile1.txt":            "this is a test file 1",
		"readme.md":                "this is a readme file",
		"b/filename-with-hypen.gz": "file has hypen in its name",
		"a/another_test_file.txt":  "yet another txt file. yatf.",
	}

	// assert s3
	for key, content := range expectedS3Content {
		assert.Assert(t, ensureS3Object(s3client, bucket, key, content))
	}
}

// sync  s3://bucket/* folder/
func TestSyncS3BucketToEmptyFolder(t *testing.T) {
	t.Parallel()
	s3client, s5cmd, cleanup := setup(t)
	defer cleanup()

	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	S3Content := map[string]string{
		"testfile1.txt":           "this is a test file 1",
		"readme.md":               "this is a readme file",
		"a/another_test_file.txt": "yet another txt file. yatf.",
		"abc/def/test.py":         "file in nested folders",
	}

	for filename, content := range S3Content {
		putFile(t, s3client, bucket, filename, content)
	}

	workdir := fs.NewDir(t, "somedir")
	defer workdir.Remove()

	bucketPath := fmt.Sprintf("s3://%v", bucket)
	src := fmt.Sprintf("%v/*", bucketPath)
	dst := fmt.Sprintf("%v/", workdir.Path())
	dst = filepath.ToSlash(dst)

	cmd := s5cmd("sync", src, dst)
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals(`download %v/a/another_test_file.txt %va/another_test_file.txt`, bucketPath, dst),
		1: equals(`download %v/abc/def/test.py %vabc/def/test.py`, bucketPath, dst),
		2: equals(`download %v/readme.md %vreadme.md`, bucketPath, dst),
		3: equals(`download %v/testfile1.txt %vtestfile1.txt`, bucketPath, dst),
	}, sortInput(true))

	expectedFolderLayout := []fs.PathOp{
		fs.WithFile("testfile1.txt", "this is a test file 1"),
		fs.WithFile("readme.md", "this is a readme file"),
		fs.WithDir(
			"a",
			fs.WithFile("another_test_file.txt", "yet another txt file. yatf."),
		),
		fs.WithDir("abc",
			fs.WithDir("def",
				fs.WithFile("test.py", "file in nested folders"),
			),
		),
	}

	// assert local filesystem
	expected := fs.Expected(t, expectedFolderLayout...)
	assert.Assert(t, fs.Equal(workdir.Path(), expected))

	// assert s3
	for key, content := range S3Content {
		assert.Assert(t, ensureS3Object(s3client, bucket, key, content))
	}
}

// sync folder/ s3://bucket (source older, same objects)
func TestSyncLocalFolderToS3BucketSameObjectsSourceOlder(t *testing.T) {
	t.Parallel()
	s3client, s5cmd, cleanup := setup(t)
	defer cleanup()

	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	now := time.Now().UTC()
	timestamp := fs.WithTimestamps(
		now.Add(-time.Minute), // access time
		now.Add(-time.Minute), // mod time
	)

	folderLayout := []fs.PathOp{
		fs.WithFile("main.py", "this is a python file", timestamp),
		fs.WithFile("testfile1.txt", "this is a test file 1", timestamp),
		fs.WithFile("readme.md", "this is a readme file", timestamp),
		fs.WithDir(
			"a",
			fs.WithFile("another_test_file.txt", "yet another txt file. yatf.", timestamp),
			timestamp,
		),
	}

	// for expected local structure, same as folderLayout without timestamps.
	folderLayoutWithoutTimestamp := []fs.PathOp{
		fs.WithFile("main.py", "this is a python file"),
		fs.WithFile("testfile1.txt", "this is a test file 1"),
		fs.WithFile("readme.md", "this is a readme file"),
		fs.WithDir(
			"a",
			fs.WithFile("another_test_file.txt", "yet another txt file. yatf."),
		),
	}

	workdir := fs.NewDir(t, "somedir", folderLayout...)
	defer workdir.Remove()

	S3Content := map[string]string{
		"main.py":                 "this is a python file",
		"testfile1.txt":           "this is a test file 1",
		"readme.md":               "this is a readme file",
		"a/another_test_file.txt": "yet another txt file. yatf.",
	}

	for filename, content := range S3Content {
		putFile(t, s3client, bucket, filename, content)
	}

	src := fmt.Sprintf("%v/", workdir.Path())
	src = filepath.ToSlash(src)
	dst := fmt.Sprintf("s3://%v/", bucket)

	// log debug
	cmd := s5cmd("--log", "debug", "sync", src, dst)
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals(`DEBUG "sync %va/another_test_file.txt %va/another_test_file.txt": object is newer or same age`, src, dst),
		1: equals(`DEBUG "sync %vmain.py %vmain.py": object is newer or same age`, src, dst),
		2: equals(`DEBUG "sync %vreadme.md %vreadme.md": object is newer or same age`, src, dst),
		3: equals(`DEBUG "sync %vtestfile1.txt %vtestfile1.txt": object is newer or same age`, src, dst),
	}, sortInput(true))

	// expected folder structure without the timestamp.
	expected := fs.Expected(t, folderLayoutWithoutTimestamp...)
	assert.Assert(t, fs.Equal(workdir.Path(), expected))

	// assert s3
	for key, content := range S3Content {
		assert.Assert(t, ensureS3Object(s3client, bucket, key, content))
	}
}

// sync folder/ s3://bucket (source newer)
func TestSyncLocalFolderToS3BucketSameSizeSourceNewer(t *testing.T) {
	t.Parallel()
	s3client, s5cmd, cleanup := setup(t)
	defer cleanup()

	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	now := time.Now().UTC()
	timestamp := fs.WithTimestamps(
		now.Add(time.Minute), // access time
		now.Add(time.Minute), // mod time
	)

	folderLayout := []fs.PathOp{
		fs.WithFile("testfile1.txt", "this is a test file 2", timestamp), // content different from s3
		fs.WithFile("readme.md", "this is a readve file", timestamp),     // content different from s3
		fs.WithDir("dir",
			fs.WithFile("main.py", "python file 2", timestamp), // content different from s3
			timestamp,
		),
	}

	// for expected local structure, same as folderLayout without timestamps.
	folderLayoutWithoutTimestamp := []fs.PathOp{
		fs.WithFile("testfile1.txt", "this is a test file 2"), // content different from s3
		fs.WithFile("readme.md", "this is a readve file"),     // content different from s3
		fs.WithDir("dir",
			fs.WithFile("main.py", "python file 2"), // content different from s3
		),
	}

	workdir := fs.NewDir(t, "somedir", folderLayout...)
	defer workdir.Remove()

	S3Content := map[string]string{
		"testfile1.txt": "this is a test file 1",
		"readme.md":     "this is a readme file",
		"dir/main.py":   "python file 1",
	}

	for filename, content := range S3Content {
		putFile(t, s3client, bucket, filename, content)
	}

	src := fmt.Sprintf("%v/", workdir.Path())
	src = filepath.ToSlash(src)
	dst := fmt.Sprintf("s3://%v/", bucket)

	// log debug
	cmd := s5cmd("--log", "debug", "sync", src, dst)
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals(`upload %vdir/main.py %vdir/main.py`, src, dst),
		1: equals(`upload %vreadme.md %vreadme.md`, src, dst),
		2: equals(`upload %vtestfile1.txt %vtestfile1.txt`, src, dst),
	}, sortInput(true))

	// expected folder structure without the timestamp.
	expected := fs.Expected(t, folderLayoutWithoutTimestamp...)
	assert.Assert(t, fs.Equal(workdir.Path(), expected))

	expectedS3Content := map[string]string{
		"testfile1.txt": "this is a test file 2", // same as local source
		"readme.md":     "this is a readve file", // same as local source
		"dir/main.py":   "python file 2",         // same as local source
	}

	// assert s3
	for key, content := range expectedS3Content {
		assert.Assert(t, ensureS3Object(s3client, bucket, key, content))
	}
}

// sync s3://bucket/* folder/ (source older, same objects)
func TestSyncS3BucketToLocalFolderSameObjectsSourceOlder(t *testing.T) {
	t.Parallel()
	s3client, s5cmd, cleanup := setup(t)
	defer cleanup()

	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	// timestamp for local folder, local ise newer.
	now := time.Now().UTC()
	timestamp := fs.WithTimestamps(
		now.Add(time.Minute), // access time
		now.Add(time.Minute), // mod time
	)

	folderLayout := []fs.PathOp{
		fs.WithFile("main.py", "this is a python file", timestamp),
		fs.WithFile("testfile1.txt", "this is a test file 1", timestamp),
		fs.WithFile("readme.md", "this is a readme file", timestamp),
		fs.WithDir(
			"a",
			fs.WithFile("another_test_file.txt", "yet another txt file. yatf.", timestamp),
			timestamp,
		),
	}

	// for expected local structure, same as folderLayout without timestamps.
	// content should not be changed.
	expectedLayout := []fs.PathOp{
		fs.WithFile("main.py", "this is a python file"),
		fs.WithFile("testfile1.txt", "this is a test file 1"),
		fs.WithFile("readme.md", "this is a readme file"),
		fs.WithDir(
			"a",
			fs.WithFile("another_test_file.txt", "yet another txt file. yatf."),
		),
	}

	workdir := fs.NewDir(t, "somedir", folderLayout...)
	defer workdir.Remove()

	S3Content := map[string]string{
		"main.py":                 "this is a python file",
		"testfile1.txt":           "this is a test file 2",       // content different from local
		"readme.md":               "this is a readme file",       // content different from local
		"a/another_test_file.txt": "yet another txt file. yatg.", // content different from local
	}

	for filename, content := range S3Content {
		putFile(t, s3client, bucket, filename, content)
	}

	bucketPath := fmt.Sprintf("s3://%v", bucket)
	src := fmt.Sprintf("%s/*", bucketPath)
	dst := fmt.Sprintf("%v/", workdir.Path())
	dst = filepath.ToSlash(dst)

	// log debug
	cmd := s5cmd("--log", "debug", "sync", src, dst)
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals(`DEBUG "sync %v/a/another_test_file.txt %va/another_test_file.txt": object is newer or same age`, bucketPath, dst),
		1: equals(`DEBUG "sync %v/main.py %vmain.py": object is newer or same age`, bucketPath, dst),
		2: equals(`DEBUG "sync %v/readme.md %vreadme.md": object is newer or same age`, bucketPath, dst),
		3: equals(`DEBUG "sync %v/testfile1.txt %vtestfile1.txt": object is newer or same age`, bucketPath, dst),
	}, sortInput(true))

	// expected folder structure without the timestamp.
	expected := fs.Expected(t, expectedLayout...)
	assert.Assert(t, fs.Equal(workdir.Path(), expected))

	// assert s3
	for key, content := range S3Content {
		assert.Assert(t, ensureS3Object(s3client, bucket, key, content))
	}
}

// sync s3://bucket/* folder/ (source newer, same objects)
func TestSyncS3BucketToLocalFolderSameObjectsSourceNewer(t *testing.T) {
	t.Parallel()
	s3client, s5cmd, cleanup := setup(t)
	defer cleanup()

	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	// timestamp for local folder, source ise newer.
	now := time.Now().UTC()
	timestamp := fs.WithTimestamps(
		now.Add(-time.Minute), // access time
		now.Add(-time.Minute), // mod time
	)

	folderLayout := []fs.PathOp{
		fs.WithFile("main.py", "this is a python file", timestamp),
		fs.WithFile("testfile1.txt", "this is a test file 1", timestamp),
		fs.WithFile("readme.md", "this is a readme file", timestamp),
		fs.WithDir(
			"a",
			fs.WithFile("another_test_file.txt", "yet another txt file. yatf.", timestamp),
			timestamp,
		),
	}

	// for expected local structure, same as folderLayout without timestamps.
	// content should be same as s3 contents.
	expectedLayout := []fs.PathOp{
		fs.WithFile("main.py", "this is a python file"),
		fs.WithFile("testfile1.txt", "this is a test file 2"),
		fs.WithFile("readme.md", "this is a readve file"),
		fs.WithDir(
			"a",
			fs.WithFile("another_test_file.txt", "yet another txt file. yatg:"),
		),
	}

	workdir := fs.NewDir(t, "somedir", folderLayout...)
	defer workdir.Remove()

	S3Content := map[string]string{
		"main.py":                 "this is a python file",
		"testfile1.txt":           "this is a test file 2",       // content different from local
		"readme.md":               "this is a readve file",       // content different from local
		"a/another_test_file.txt": "yet another txt file. yatg:", // content different from local
	}

	for filename, content := range S3Content {
		putFile(t, s3client, bucket, filename, content)
	}

	bucketPath := fmt.Sprintf("s3://%v", bucket)
	src := fmt.Sprintf("%s/*", bucketPath)
	dst := fmt.Sprintf("%v/", workdir.Path())
	dst = filepath.ToSlash(dst)

	// log debug
	cmd := s5cmd("--log", "debug", "sync", src, dst)
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals(`download %v/a/another_test_file.txt %va/another_test_file.txt`, bucketPath, dst),
		1: equals(`download %v/main.py %vmain.py`, bucketPath, dst),
		2: equals(`download %v/readme.md %vreadme.md`, bucketPath, dst),
		3: equals(`download %v/testfile1.txt %vtestfile1.txt`, bucketPath, dst),
	}, sortInput(true))

	// expected folder structure without the timestamp.
	expected := fs.Expected(t, expectedLayout...)
	assert.Assert(t, fs.Equal(workdir.Path(), expected))

	// assert s3
	for key, content := range S3Content {
		assert.Assert(t, ensureS3Object(s3client, bucket, key, content))
	}
}

// sync --size-only s3://bucket/* folder/
func TestSyncS3BucketToLocalFolderSameObjectsSizeOnly(t *testing.T) {
	t.Parallel()
	s3client, s5cmd, cleanup := setup(t)
	defer cleanup()

	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	folderLayout := []fs.PathOp{
		fs.WithFile("test.py", "this is a python file"),
		fs.WithFile("testfile1.txt", "this is a test file 1"),
		fs.WithFile("readme.md", "this is a readme file"),
		fs.WithDir(
			"a",
			fs.WithFile("another_test_file.txt", "yet another txt file. yatf."),
		),
	}

	workdir := fs.NewDir(t, "somedir", folderLayout...)
	defer workdir.Remove()

	S3Content := map[string]string{
		"test.py":                 "this is a python file with some extension", // content different from local, different size
		"testfile1.txt":           "this is a test file 2",                     // content different from local, same size
		"readme.md":               "this is a readve file",                     // content different from local, same size
		"a/another_test_file.txt": "yet another txt file. yatg.",               // content different from local, same size
		"abc/def/main.py":         "python file",                               // local does not have it.
	}

	for filename, content := range S3Content {
		putFile(t, s3client, bucket, filename, content)
	}

	bucketPath := fmt.Sprintf("s3://%v", bucket)
	src := fmt.Sprintf("%s/*", bucketPath)
	dst := fmt.Sprintf("%v/", workdir.Path())
	dst = filepath.ToSlash(dst)

	// log debug
	cmd := s5cmd("--log", "debug", "sync", "--size-only", src, dst)
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals(`DEBUG "sync %v/a/another_test_file.txt %va/another_test_file.txt": object size matches`, bucketPath, dst),
		1: equals(`DEBUG "sync %v/readme.md %vreadme.md": object size matches`, bucketPath, dst),
		2: equals(`DEBUG "sync %v/testfile1.txt %vtestfile1.txt": object size matches`, bucketPath, dst),
		3: equals(`download %v/abc/def/main.py %vabc/def/main.py`, bucketPath, dst),
		4: equals(`download %v/test.py %vtest.py`, bucketPath, dst),
	}, sortInput(true))

	expectedFolderLayout := []fs.PathOp{
		fs.WithFile("test.py", "this is a python file with some extension"),
		fs.WithFile("testfile1.txt", "this is a test file 1"),
		fs.WithFile("readme.md", "this is a readme file"),
		fs.WithDir("a",
			fs.WithFile("another_test_file.txt", "yet another txt file. yatf."),
		),
		fs.WithDir("abc",
			fs.WithDir("def",
				fs.WithFile("main.py", "python file"),
			),
		),
	}

	// expected folder structure without the timestamp.
	expected := fs.Expected(t, expectedFolderLayout...)
	assert.Assert(t, fs.Equal(workdir.Path(), expected))

	// assert s3
	for key, content := range S3Content {
		assert.Assert(t, ensureS3Object(s3client, bucket, key, content))
	}
}

// sync --size-only folder/ s3://bucket/
func TestSyncLocalFolderToS3BucketSameObjectsSizeOnly(t *testing.T) {
	t.Parallel()
	s3client, s5cmd, cleanup := setup(t)
	defer cleanup()

	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	folderLayout := []fs.PathOp{
		fs.WithFile("test.py", "this is a python file"),       // remote has it, different content, size same
		fs.WithFile("testfile1.txt", "this is a test file 1"), // remote has it, size different.
		fs.WithFile("readme.md", "this is a readme file"),     // remote has it, same object.
		fs.WithDir(
			"a",
			fs.WithFile("another_test_file.txt", "yet another txt file. yatf."), // remote has it, different content, same size.
		),
		fs.WithDir("abc",
			fs.WithDir("def",
				fs.WithFile("main.py", "python file"), // remote does not have it
			),
		),
	}

	workdir := fs.NewDir(t, "somedir", folderLayout...)
	defer workdir.Remove()

	S3Content := map[string]string{
		"test.py":                 "this is a python abcd",
		"testfile1.txt":           "this is a test file 100",
		"readme.md":               "this is a readme file",
		"a/another_test_file.txt": "yet another txt file. yatg.",
	}

	for filename, content := range S3Content {
		putFile(t, s3client, bucket, filename, content)
	}

	src := fmt.Sprintf("%v/", workdir.Path())
	src = filepath.ToSlash(src)
	dst := fmt.Sprintf("s3://%s/", bucket)

	// log debug
	cmd := s5cmd("--log", "debug", "sync", "--size-only", src, dst)
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals(`DEBUG "sync %va/another_test_file.txt %va/another_test_file.txt": object size matches`, src, dst),
		1: equals(`DEBUG "sync %vreadme.md %vreadme.md": object size matches`, src, dst),
		2: equals(`DEBUG "sync %vtest.py %vtest.py": object size matches`, src, dst),
		3: equals(`upload %vabc/def/main.py %vabc/def/main.py`, src, dst),
		4: equals(`upload %vtestfile1.txt %vtestfile1.txt`, src, dst),
	}, sortInput(true))

	// expected folder structure without the timestamp.
	expected := fs.Expected(t, folderLayout...)
	assert.Assert(t, fs.Equal(workdir.Path(), expected))

	expectedS3Content := map[string]string{
		"test.py":                 "this is a python abcd",
		"testfile1.txt":           "this is a test file 1",
		"readme.md":               "this is a readme file",
		"a/another_test_file.txt": "yet another txt file. yatg.",
		"abc/def/main.py":         "python file",
	}

	// assert s3
	for key, content := range expectedS3Content {
		assert.Assert(t, ensureS3Object(s3client, bucket, key, content))
	}
}

// sync --delete s3://bucket/* .
func TestSyncS3BucketToLocalWithDelete(t *testing.T) {
	t.Parallel()
	s3client, s5cmd, cleanup := setup(t)
	defer cleanup()

	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	S3Content := map[string]string{
		"testfile1.txt": "this is a test file 1",
		"readme.md":     "this is a readme file",
	}

	for filename, content := range S3Content {
		putFile(t, s3client, bucket, filename, content)
	}

	folderLayout := []fs.PathOp{
		fs.WithFile("testfile1.txt", "this is a test file 1"),
		fs.WithFile("readme.md", "this is a readme file"),
		fs.WithDir(
			"dir",
			fs.WithFile("main.py", "python file"),
		),
	}

	workdir := fs.NewDir(t, "somedir", folderLayout...)
	defer workdir.Remove()

	dst := fmt.Sprintf("%v/", workdir.Path())
	dst = filepath.ToSlash(dst)
	src := fmt.Sprintf("s3://%v/", bucket)

	cmd := s5cmd("sync", "--delete", "--size-only", src+"*", dst)
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals(`delete %vdir/main.py`, dst),
	}, sortInput(true))

	expectedFolderLayout := []fs.PathOp{
		fs.WithFile("testfile1.txt", "this is a test file 1"),
		fs.WithFile("readme.md", "this is a readme file"),
		fs.WithDir(
			"dir",
		),
	}

	// assert local filesystem
	expected := fs.Expected(t, expectedFolderLayout...)
	assert.Assert(t, fs.Equal(workdir.Path(), expected))

	// assert s3
	for key, content := range S3Content {
		assert.Assert(t, ensureS3Object(s3client, bucket, key, content))
	}
}
