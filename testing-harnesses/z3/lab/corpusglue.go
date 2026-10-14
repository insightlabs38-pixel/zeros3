package lab

import "zeros3-z3/corpus"

func corpusManifest(dir string) (*corpus.Manifest, error) { return corpus.ReadManifest(dir) }

func corpusMemberPath(dir string, m corpus.Member) (string, error) { return corpus.MemberPath(dir, m) }
