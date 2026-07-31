---
name: unclosed-fence
description: Fixture for the silencer — one missing closing fence must not hide the rest of the file.
---

# Unclosed fence fixture

```bash
echo "this block is never closed"

Everything below here used to be blanked out and read by nothing:

<<<<<<< HEAD
one side
=======
the other side
>>>>>>> other

{{UNFILLED}}

<!-- REPEAT:step -->
