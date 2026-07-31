---
name: nofmclose
description: Fixture for the frontmatter bound — this file opens frontmatter and never closes it anywhere.

# No-close fixture

There is no second `---` in this file at all, so the scan has to give up at
its bound rather than run to EOF treating the whole file as frontmatter.
