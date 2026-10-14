package classify

import "testing"

func TestCategory(t *testing.T) {
	cases := map[string]Cat{
		"sample_R1.fastq":    CatFastq,
		"sample_R1.FQ":       CatFastq,
		"sample_R1.fastq.gz": CatFastqGz,
		"sample_R1.fq.zst":   CatFastqGz,
		"run.ora":            CatFastqGz,
		"aln.sam":            CatSam,
		"aln.sorted.bam":     CatBam,
		"aln.cram":           CatCram,
		"aln.bam.bai":        CatAlnIndex,
		"calls.vcf":          CatVcf,
		"calls.vcf.gz":       CatVcfGz,
		"calls.vcf.gz.tbi":   CatAlnIndex,
		"GRCh38.fa":          CatFasta,
		"GRCh38.fa.gz":       CatFastaGz,
		"SRR000001.sra":      CatSra,
		"reads.pod5":         CatRawSignal,
		"genes.gtf":          CatAnnot,
		"genes.gtf.gz":       CatAnnotGz,
		"ref.fa.bwt":         CatRefIndex,
		"SAindex":            CatRefIndex,
		"counts.h5ad":        CatHDF5,
		"table.tsv":          CatText,
		"table.tsv.gz":       CatTextGz,
		"backup.tar":         CatTarRaw,
		"backup.tar.gz":      CatArchive,
		"backup.tar.zst":     CatArchive,
		"tool.sif":           CatImage,
		"model.pt":           CatPyData,
		"core":               CatCoreDump,
		"core.12345":         CatCoreDump,
		"mystery.gz":         CatGzOther,
		"README":             CatOther,
		".bashrc":            CatOther,
		"weird.":             CatOther,
		"sample.unknownext":  CatOther,
	}
	for name, want := range cases {
		if got := Category(name); got != want {
			t.Errorf("Category(%q) = %s, want %s", name, Info(got).Key, Info(want).Key)
		}
	}
}

func TestCompressibleCategoriesHaveSavings(t *testing.T) {
	for _, c := range All() {
		if (c.Compress != "") != (c.SaveFrac > 0) {
			t.Errorf("%s: Compress=%q but SaveFrac=%v", c.Key, c.Compress, c.SaveFrac)
		}
		if c.SaveFrac > 1 {
			t.Errorf("%s: SaveFrac %v > 1", c.Key, c.SaveFrac)
		}
	}
}

func TestDirContext(t *testing.T) {
	type tc struct {
		name, parent, grand string
		pc                  Class
		want                Class
	}
	cases := []tc{
		{"SRR1234567", "raw", "proj", ClassNone, ClassPublic},
		{"PRJNA123456", "data", "", ClassNone, ClassPublic},
		{"GCF_000001405.40", "genomes", "", ClassNone, ClassPublic},
		{"GRCh38", "refs", "", ClassNone, ClassRefGenome},
		{"hg38_bundle", "refs", "", ClassNone, ClassRefGenome},
		{".snakemake", "proj", "", ClassNone, ClassTemp},
		{"0123456789abcdef0123456789abcd", "a1", "work", ClassNone, ClassTemp},
		{"0123456789abcdef0123456789abcd", "a1", "results", ClassNone, ClassNone},
		{"work", "proj", "", ClassNone, ClassNone},
		{"miniconda3", "user", "home", ClassNone, ClassSoftware},
		{".Trash-1000", "user", "", ClassNone, ClassTrash},
		// inherited
		{"sub", "SRR1234567", "", ClassPublic, ClassPublic},
		// temp under public stays temp; public under temp stays temp
		{"tmp", "SRR1234567", "", ClassPublic, ClassTemp},
		{"SRR1234567", "tmp", "", ClassTemp, ClassTemp},
		// plain names that must not match
		{"SRR12", "x", "", ClassNone, ClassNone},
		{"results", "proj", "", ClassNone, ClassNone},
		{"thg38x", "proj", "", ClassNone, ClassNone},
	}
	for _, c := range cases {
		if got := DirContext(c.name, c.parent, c.grand, c.pc); got != c.want {
			t.Errorf("DirContext(%q,%q,%q,%s) = %s, want %s", c.name, c.parent, c.grand,
				ClassMeta(c.pc).Key, ClassMeta(got).Key, ClassMeta(c.want).Key)
		}
	}
}

func TestFileClass(t *testing.T) {
	type tc struct {
		name string
		dir  Class
		want Class
	}
	cases := []tc{
		{"SRR1234567_1.fastq.gz", ClassNone, ClassPublic},
		{"ERR000001.fastq.gz", ClassNone, ClassPublic},
		{"my_sample_R1.fastq.gz", ClassNone, ClassNone},
		{"reads.sra", ClassNone, ClassPublic},
		{"core.999", ClassNone, ClassTemp},
		{"notes.txt~", ClassNone, ClassTrash},
		{"data.bak", ClassPublic, ClassTrash},
		{"x.bam", ClassTemp, ClassTemp},
		{"tool.sif", ClassNone, ClassSoftware},
		{"Homo_sapiens.GRCh38.dna.fa.gz", ClassNone, ClassRefGenome},
		{"reads.fastq", ClassPublic, ClassPublic},
	}
	for _, c := range cases {
		cat := Category(c.name)
		if got := FileClass(c.name, cat, c.dir); got != c.want {
			t.Errorf("FileClass(%q, dir=%s) = %s, want %s", c.name, ClassMeta(c.dir).Key, ClassMeta(got).Key, ClassMeta(c.want).Key)
		}
	}
}

func TestExtractAccession(t *testing.T) {
	cases := map[string]string{
		"SRR1234567_1.fastq.gz":              "SRR1234567",
		"/data/GCF_000001405.40_GRCh38/x.fa": "GCF_000001405.40",
		"sample.fastq":                       "",
	}
	for in, want := range cases {
		if got := ExtractAccession(in); got != want {
			t.Errorf("ExtractAccession(%q) = %q, want %q", in, got, want)
		}
	}
}
