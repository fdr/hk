package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/bgentry/heroku-go"
)

var releaseCount int

var cmdReleases = &Command{
	Run:      runReleases,
	Usage:    "releases [<version>...]",
	NeedsApp: true,
	Category: "release",
	Short:    "list releases",
	Long: `
Lists releases. Shows the version of the release (e.g. v1), who
made the release, git commit id, time of the release, and
description.

Examples:

    $ hk releases
    v1  bob@test.com  3ae20c2  Jun 12 18:28  Deploy 3ae20c2
    v2  john@me.com   0fda0ae  Jun 13 18:14  Deploy 0fda0ae
    v3  john@me.com            Jun 13 18:31  Rollback to v2

    $ hk releases -n 2
    v2  john  0fda0ae  Jun 13 18:14  Deploy 0fda0ae
    v3  john           Jun 13 18:31  Rollback to v2

    $ hk releases 1 3
    v1  bob@test.com  3ae20c2  Jun 12 18:28  Deploy 3ae20c2
    v3  john@me.com            Jun 13 18:31  Rollback to v2
`,
}

func init() {
	cmdReleases.Flag.IntVar(&releaseCount, "n", 30, "max number of recent releases to display")
}

func runReleases(cmd *Command, versions []string) {
	w := tabwriter.NewWriter(os.Stdout, 1, 2, 2, ' ', 0)
	defer w.Flush()
	listReleases(w, versions)
}

func listReleases(w io.Writer, versions []string) {
	appname := mustApp()
	if len(versions) == 0 {
		hrels, err := client.ReleaseList(appname, &heroku.ListRange{
			Field:      "version",
			Max:        releaseCount,
			Descending: true,
		})
		must(err)
		rels := make([]*Release, len(hrels))
		for i := range hrels {
			rels[i] = newRelease(&hrels[i])
		}
		sort.Sort(releasesByVersion(rels))
		gitDescribe(rels)
		abbrevEmailReleases(rels)
		for _, r := range rels {
			listRelease(w, r)
		}
		return
	}

	var rels []*Release
	relch := make(chan *heroku.Release, len(versions))
	errch := make(chan error, len(versions))
	for _, name := range versions {
		if name == "" {
			relch <- nil
		} else {
			go func(relname string) {
				if rel, err := client.ReleaseInfo(appname, relname); err != nil {
					errch <- err
				} else {
					relch <- rel
				}
			}(strings.TrimPrefix(name, "v"))
		}
	}
	for _ = range versions {
		select {
		case err := <-errch:
			printFatal(err.Error())
		case rel := <-relch:
			if rel != nil {
				rels = append(rels, newRelease(rel))
			}
		}
	}
	sort.Sort(releasesByVersion(rels))
	gitDescribe(rels)
	abbrevEmailReleases(rels)
	for _, r := range rels {
		listRelease(w, r)
	}
}

func abbrevEmailReleases(rels []*Release) {
	domains := make(map[string]int)
	for _, r := range rels {
		r.Who = r.User.Email
		if a := strings.SplitN(r.Who, "@", 2); len(a) == 2 {
			domains["@"+a[1]]++
		}
	}
	smax, nmax := "", 0
	for s, n := range domains {
		if n > nmax {
			smax = s
		}
	}
	for _, r := range rels {
		if strings.HasSuffix(r.Who, smax) {
			r.Who = r.Who[:len(r.Who)-len(smax)]
		}
	}
}

func listRelease(w io.Writer, r *Release) {
	listRec(w,
		fmt.Sprintf("v%d", r.Version),
		abbrev(r.Who, 10),
		abbrev(r.Commit, 10),
		prettyTime{r.CreatedAt},
		r.Description,
	)
}

type releasesByVersion []*Release

func (a releasesByVersion) Len() int           { return len(a) }
func (a releasesByVersion) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }
func (a releasesByVersion) Less(i, j int) bool { return a[i].Version < a[j].Version }

func newRelease(rel *heroku.Release) *Release {
	return &Release{*rel, "", ""}
}

var cmdReleaseInfo = &Command{
	Run:      runReleaseInfo,
	Usage:    "release-info <version>",
	NeedsApp: true,
	Category: "release",
	Short:    "show release info",
	Long: `
release-info shows detailed information about a release.

Examples:

    $ hk release-info v116
    Version:  v116
    By:       user@test.com
    Change:   Deploy 62b3059
    When:     2014-01-13T21:20:57Z
    Id:       abcd1234-5678-def0-8190-12347060474d
    Slug:     98765432-82ba-10ba-fedc-8d206789d062
`,
}

func runReleaseInfo(cmd *Command, args []string) {
	appname := mustApp()
	if len(args) != 1 {
		cmd.printUsage()
		os.Exit(2)
	}
	ver := strings.TrimPrefix(args[0], "v")
	rel, err := client.ReleaseInfo(appname, ver)
	must(err)

	fmt.Printf("Version:  v%d\n", rel.Version)
	fmt.Printf("By:       %s\n", rel.User.Email)
	fmt.Printf("Change:   %s\n", rel.Description)
	fmt.Printf("When:     %s\n", rel.CreatedAt.UTC().Format(time.RFC3339))
	fmt.Printf("Id:       %s\n", rel.Id)
	fmt.Printf("Slug:     %s\n", rel.Slug.Id)
}

var cmdRollback = &Command{
	Run:      runRollback,
	Usage:    "rollback <version>",
	NeedsApp: true,
	Category: "release",
	Short:    "roll back to a previous release",
	Long: `
Rollback re-releases an app at an older version. This action
creates a new release based on the older release, then restarts
the app's dynos on the new release.

Examples:

    $ hk rollback v4
    Rolled back myapp to v4 as v7.
`,
}

func runRollback(cmd *Command, args []string) {
	appname := mustApp()
	if len(args) != 1 {
		cmd.printUsage()
		os.Exit(2)
	}
	ver := strings.TrimPrefix(args[0], "v")
	rel, err := client.ReleaseRollback(appname, ver)
	must(err)
	log.Printf("Rolled back %s to v%s as v%d.\n", appname, ver, rel.Version)
}
