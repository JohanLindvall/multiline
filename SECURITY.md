# Security policy

## Reporting a vulnerability

Please do not open a public issue for a security problem. Report it privately
through GitHub's
[Report a vulnerability](https://github.com/JohanLindvall/multiline/security/advisories/new)
form, under this repository's *Security* tab. The report stays private between
you and the maintainer until a fix is released and the advisory is published;
you are credited in it unless you would rather not be.

A useful report names the version or commit, the options passed to `New` (or
`cri.New`), and the shortest sequence of input lines that reproduces the
problem — for a crash, include the panic and its stack trace.

## Supported versions

`multiline` is pre-1.0 and released continuously: every green build of `main`
is tagged as the next `v0.0.x` version. A fix ships in the next tag; earlier
tags are not patched, so upgrade to the latest version to pick it up.

## Scope

The library parses untrusted text — anything that can write to a log can feed
it lines — so a vulnerability is anything such input can trigger:

- a panic, or processing time that grows faster than linearly with the input;
- buffered memory growing past the limits set with `WithMaxLines`,
  `WithMaxBytes`, `WithMaxGroups` or `WithMaxTotalBytes`;
- text from one key ending up in an entry for another key.

Other wrong output — a trace that is not joined, or joined with a line it
should not have been — is an ordinary bug; please open a public issue for it.
