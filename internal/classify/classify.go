// Package classify assigns files to data categories (fastq, bam, ...) and
// "reclaim classes" (public NCBI/ENA data, pipeline temp, software envs).
//
// All rules are filename/path heuristics. Savings factors are rough, typical
// values for genomic data — they are labelled as estimates in the UI.
package classify

import (
	"regexp"
	"strings"
)

// Cat is a file category id. Order is stable: it is persisted in index files.
type Cat uint8

const (
	CatOther Cat = iota
	CatDirMeta
	CatFastq
	CatFastqGz
	CatSam
	CatBam
	CatCram
	CatAlnIndex // .bai .crai .csi .tbi .fai .dict
	CatVcf
	CatVcfGz
	CatFasta
	CatFastaGz
	CatSra
	CatRawSignal // fast5 pod5 blow5 slow5
	CatAnnot     // bed gff gtf wig bedgraph (uncompressed)
	CatAnnotGz
	CatBigBinary // bigwig bigbed, hic, cool
	CatRefIndex  // aligner indexes: bwa, bowtie2, hisat2, minimap2, STAR
	CatHDF5      // h5 h5ad loom hdf5 mtx(sc)
	CatText      // tsv csv txt log json (uncompressed)
	CatTextGz
	CatArchive  // tar zip tgz 7z
	CatTarRaw   // uncompressed .tar
	CatImage    // sif simg container images, qcow, iso
	CatPyData   // npy npz pkl pt ckpt rds rdata parquet
	CatMedia    // png jpg tif pdf
	CatCode     // py r sh c go ...
	CatGzOther  // .gz/.zst/.bz2/.xz of something we don't recognise
	CatCoreDump // core, core.12345
	numCats
)

// CatInfo describes a category for the UI.
type CatInfo struct {
	ID    Cat    `json:"id"`
	Key   string `json:"key"`
	Label string `json:"label"`
	// Compress describes how the category can be made smaller; empty if not.
	Compress string `json:"compress,omitempty"`
	// SaveFrac is the estimated fraction of bytes freed by Compress (0..1).
	SaveFrac float64 `json:"save_frac,omitempty"`
}

var cats = [numCats]CatInfo{
	CatOther:     {Key: "other", Label: "Прочее"},
	CatDirMeta:   {Key: "dirmeta", Label: "Служебное (каталоги, симлинки)"},
	CatFastq:     {Key: "fastq", Label: "FASTQ (несжатый)", Compress: "gzip/zstd (или Spring/ORA) — несжатый FASTQ почти не нужен", SaveFrac: 0.75},
	CatFastqGz:   {Key: "fastq_gz", Label: "FASTQ (сжатый)"},
	CatSam:       {Key: "sam", Label: "SAM", Compress: "samtools view -b → BAM (или сразу CRAM)", SaveFrac: 0.75},
	CatBam:       {Key: "bam", Label: "BAM", Compress: "samtools view -C → CRAM (нужен референс)", SaveFrac: 0.45},
	CatCram:      {Key: "cram", Label: "CRAM"},
	CatAlnIndex:  {Key: "aln_index", Label: "Индексы (bai/crai/tbi/fai)"},
	CatVcf:       {Key: "vcf", Label: "VCF (несжатый)", Compress: "bgzip + tabix (или BCF)", SaveFrac: 0.85},
	CatVcfGz:     {Key: "vcf_gz", Label: "VCF.gz / BCF"},
	CatFasta:     {Key: "fasta", Label: "FASTA (несжатый)", Compress: "bgzip (samtools faidx работает с bgzip)", SaveFrac: 0.70},
	CatFastaGz:   {Key: "fasta_gz", Label: "FASTA (сжатый)"},
	CatSra:       {Key: "sra", Label: "SRA (.sra/.sralite)"},
	CatRawSignal: {Key: "raw_signal", Label: "Сырой сигнал ONT (fast5/pod5/blow5)", Compress: "fast5 → pod5 или blow5 (vbz/zstd)", SaveFrac: 0.30},
	CatAnnot:     {Key: "annot", Label: "Аннотации/треки (bed/gff/gtf/wig, несжатые)", Compress: "bgzip + tabix; wig/bedgraph → bigWig", SaveFrac: 0.80},
	CatAnnotGz:   {Key: "annot_gz", Label: "Аннотации/треки (сжатые)"},
	CatBigBinary: {Key: "big_binary", Label: "bigWig/bigBed/hic/cool"},
	CatRefIndex:  {Key: "ref_index", Label: "Индексы выравнивателей (bwa/bowtie2/STAR/...)"},
	CatHDF5:      {Key: "hdf5", Label: "HDF5 / h5ad / loom / mtx"},
	CatText:      {Key: "text", Label: "Текст/таблицы (tsv/csv/txt/log, несжатые)", Compress: "gzip/zstd, если это не рабочие файлы", SaveFrac: 0.75},
	CatTextGz:    {Key: "text_gz", Label: "Текст/таблицы (сжатые)"},
	CatArchive:   {Key: "archive", Label: "Архивы (tar.gz/zip/7z)"},
	CatTarRaw:    {Key: "tar_raw", Label: "Несжатые .tar", Compress: "zstd -T0 (или распаковать/удалить дубликат)", SaveFrac: 0.50},
	CatImage:     {Key: "image", Label: "Образы контейнеров/ВМ (sif/img/iso/qcow2)"},
	CatPyData:    {Key: "pydata", Label: "Бинарные данные анализа (npy/pkl/rds/parquet/pt)"},
	CatMedia:     {Key: "media", Label: "Картинки/PDF"},
	CatCode:      {Key: "code", Label: "Код/скрипты"},
	CatGzOther:   {Key: "gz_other", Label: "Прочие сжатые (.gz/.zst/.bz2/.xz)"},
	CatCoreDump:  {Key: "core", Label: "Core dump", Compress: "удалить — это аварийные дампы процессов", SaveFrac: 1.0},
}

func init() {
	for i := range cats {
		cats[i].ID = Cat(i)
	}
}

// NumCats is the number of categories.
const NumCats = int(numCats)

// Info returns category metadata.
func Info(c Cat) CatInfo {
	if int(c) >= len(cats) {
		return CatInfo{ID: c, Key: "unknown", Label: "?"}
	}
	return cats[c]
}

// All returns every category.
func All() []CatInfo { return cats[:] }

var exts = map[string]Cat{
	"fastq": CatFastq, "fq": CatFastq,
	"sam": CatSam, "bam": CatBam, "cram": CatCram,
	"bai": CatAlnIndex, "crai": CatAlnIndex, "csi": CatAlnIndex, "tbi": CatAlnIndex, "fai": CatAlnIndex, "dict": CatAlnIndex, "gzi": CatAlnIndex, "pbi": CatAlnIndex,
	"vcf": CatVcf, "bcf": CatVcfGz, "gvcf": CatVcf,
	"fa": CatFasta, "fasta": CatFasta, "fna": CatFasta, "ffn": CatFasta, "faa": CatFasta, "fas": CatFasta, "frn": CatFasta, "2bit": CatFastaGz,
	"sra": CatSra, "sralite": CatSra,
	"fast5": CatRawSignal, "pod5": CatRawSignal, "blow5": CatRawSignal, "slow5": CatRawSignal,
	"bed": CatAnnot, "gff": CatAnnot, "gff3": CatAnnot, "gtf": CatAnnot, "wig": CatAnnot, "bedgraph": CatAnnot, "bdg": CatAnnot, "psl": CatAnnot, "paf": CatAnnot, "maf": CatAnnot, "sff": CatAnnot, "pileup": CatAnnot, "mpileup": CatAnnot,
	"bw": CatBigBinary, "bigwig": CatBigBinary, "bb": CatBigBinary, "bigbed": CatBigBinary, "hic": CatBigBinary, "cool": CatBigBinary, "mcool": CatBigBinary, "pairs": CatAnnot,
	"bt2": CatRefIndex, "bt2l": CatRefIndex, "ht2": CatRefIndex, "ht2l": CatRefIndex, "mmi": CatRefIndex, "bwt": CatRefIndex, "pac": CatRefIndex, "ann": CatRefIndex, "amb": CatRefIndex, "sa": CatRefIndex, "ebwt": CatRefIndex, "0123": CatRefIndex, "idx": CatRefIndex, "k2d": CatRefIndex, "dmnd": CatRefIndex, "nhr": CatRefIndex, "nin": CatRefIndex, "nsq": CatRefIndex, "phr": CatRefIndex, "pin": CatRefIndex, "psq": CatRefIndex, "ndb": CatRefIndex, "not": CatRefIndex, "ntf": CatRefIndex, "nto": CatRefIndex,
	"h5": CatHDF5, "hdf5": CatHDF5, "h5ad": CatHDF5, "loom": CatHDF5, "mtx": CatHDF5, "zarr": CatHDF5, "h5mu": CatHDF5,
	"tsv": CatText, "csv": CatText, "txt": CatText, "log": CatText, "json": CatText, "out": CatText, "err": CatText, "tab": CatText, "xml": CatText, "html": CatText, "sql": CatText, "jsonl": CatText, "mat": CatText,
	"tgz": CatArchive, "zip": CatArchive, "7z": CatArchive, "rar": CatArchive, "tbz2": CatArchive, "txz": CatArchive,
	"tar": CatTarRaw,
	"sif": CatImage, "simg": CatImage, "img": CatImage, "iso": CatImage, "qcow2": CatImage, "vdi": CatImage, "vmdk": CatImage, "squashfs": CatImage,
	"npy": CatPyData, "npz": CatPyData, "pkl": CatPyData, "pickle": CatPyData, "pt": CatPyData, "pth": CatPyData, "ckpt": CatPyData, "safetensors": CatPyData, "rds": CatPyData, "rdata": CatPyData, "rda": CatPyData, "parquet": CatPyData, "feather": CatPyData, "arrow": CatPyData, "sqlite": CatPyData, "db": CatPyData, "joblib": CatPyData, "bin": CatPyData, "onnx": CatPyData, "gguf": CatPyData,
	"png": CatMedia, "jpg": CatMedia, "jpeg": CatMedia, "tif": CatMedia, "tiff": CatMedia, "pdf": CatMedia, "svg": CatMedia, "mp4": CatMedia, "czi": CatMedia, "nd2": CatMedia,
	"py": CatCode, "r": CatCode, "sh": CatCode, "c": CatCode, "cpp": CatCode, "h": CatCode, "go": CatCode, "rs": CatCode, "java": CatCode, "js": CatCode, "ipynb": CatCode, "nf": CatCode, "smk": CatCode, "wdl": CatCode, "pl": CatCode, "so": CatCode, "o": CatCode, "a": CatCode, "pyc": CatCode, "jar": CatCode, "whl": CatCode,
}

// compressed variants of an inner category.
var gzOf = map[Cat]Cat{
	CatFastq: CatFastqGz, CatVcf: CatVcfGz, CatFasta: CatFastaGz,
	CatAnnot: CatAnnotGz, CatText: CatTextGz, CatTarRaw: CatArchive,
	CatSam: CatBam, // sam.gz is rare but compressed
}

var compSuffix = map[string]bool{"gz": true, "bgz": true, "bz2": true, "xz": true, "zst": true, "zstd": true, "lz4": true, "ora": true, "spring": true, "dsrc": true, "fqz": true}

var reCore = regexp.MustCompile(`^core(\.\d+)?$`)

// STAR/Salmon index files without extensions.
var bareIndexNames = map[string]bool{
	"SA": true, "SAindex": true, "Genome": true, "chrName.txt": false,
	"pos.bin": true, "ctable.bin": true, "mphf.bin": true, "rank.bin": true, "seq.bin": true, "refseq.bin": true,
}

// Category classifies a file by name.
func Category(name string) Cat {
	if bareIndexNames[name] {
		return CatRefIndex
	}
	if reCore.MatchString(name) {
		return CatCoreDump
	}
	lower := strings.ToLower(name)
	ext, rest := lastExt(lower)
	if ext == "" {
		return CatOther
	}
	if compSuffix[ext] {
		if ext == "ora" || ext == "spring" || ext == "dsrc" || ext == "fqz" {
			return CatFastqGz
		}
		inner, _ := lastExt(rest)
		if c, ok := exts[inner]; ok {
			if g, ok := gzOf[c]; ok {
				return g
			}
			return c // already a compressed/binary format (e.g. bam.gz)
		}
		if inner == "tar" {
			return CatArchive
		}
		return CatGzOther
	}
	if c, ok := exts[ext]; ok {
		return c
	}
	return CatOther
}

func lastExt(name string) (ext, rest string) {
	i := strings.LastIndexByte(name, '.')
	if i <= 0 || i == len(name)-1 {
		return "", name
	}
	return name[i+1:], name[:i]
}

// Class is a "reclaim class": where data could be recovered from, or why it
// is likely disposable. Independent from Cat.
type Class uint8

const (
	ClassNone      Class = iota
	ClassPublic          // public archive accession (SRA/ENA/DDBJ run, GenBank/RefSeq assembly)
	ClassRefGenome       // well-known reference genome/annotation (downloadable)
	ClassTemp            // pipeline work dirs, tmp, caches
	ClassSoftware        // conda/mamba envs & package caches, containers caches
	ClassTrash           // .Trash, *.bak, *~ old copies
	numClasses
)

// NumClasses is the number of classes.
const NumClasses = int(numClasses)

// ClassInfo describes a reclaim class for the UI.
type ClassInfo struct {
	ID    Class  `json:"id"`
	Key   string `json:"key"`
	Label string `json:"label"`
	Hint  string `json:"hint"`
}

var classes = [numClasses]ClassInfo{
	ClassNone:      {Key: "none", Label: "—"},
	ClassPublic:    {Key: "public", Label: "Публичные данные (NCBI SRA / ENA / DDBJ / GenBank)", Hint: "В имени файла или каталога есть accession (SRR/ERR/DRR, GCA_/GCF_, PRJNA…). Такие данные можно удалить и при необходимости скачать заново: prefetch/fasterq-dump, ENA FTP, NCBI datasets. Проверьте, что это не ваши собственные данные, ещё не отправленные в архив."},
	ClassRefGenome: {Key: "refgenome", Label: "Референсные геномы и аннотации", Hint: "Похоже на стандартные референсы (GRCh38/hg19/GRCm39/T2T/Ensembl/GENCODE). Их можно держать в одной общей копии, а не у каждого пользователя."},
	ClassTemp:      {Key: "temp", Label: "Временные файлы пайплайнов и кэши", Hint: "Каталоги Nextflow work/, .snakemake, tmp, .cache, __pycache__ и core dumps. Обычно их можно удалить после того, как пайплайн успешно завершился."},
	ClassSoftware:  {Key: "software", Label: "Софт: conda/mamba/pip/singularity", Hint: "Окружения и кэши пакетов. Кэш pkgs чистится командой `conda clean -a`, старые окружения можно пересоздать из environment.yml."},
	ClassTrash:     {Key: "trash", Label: "Корзина и копии (.Trash, *.bak, *~)", Hint: "Корзины рабочих столов, резервные копии и старые версии файлов."},
}

func init() {
	for i := range classes {
		classes[i].ID = Class(i)
	}
}

// ClassMeta returns class metadata.
func ClassMeta(c Class) ClassInfo {
	if int(c) >= len(classes) {
		return ClassInfo{ID: c, Key: "unknown", Label: "?"}
	}
	return classes[c]
}

// AllClasses returns every class.
func AllClasses() []ClassInfo { return classes[:] }

var (
	// Run/experiment/sample accessions of INSDC archives and assemblies.
	reAccession = regexp.MustCompile(`(^|[^A-Za-z0-9])(([SED]RR|[SED]RX|[SED]RS|[SED]RP)\d{6,}|GC[AF]_\d{9}(\.\d+)?|PRJ(NA|EB|DB)\d{3,}|SAM(N|EA|D)\d{5,})([^0-9]|$)`)
	reRefGenome = regexp.MustCompile(`(?i)(^|[^a-z0-9])(grch3[78]|hg19|hg38|hs37d5|b37|grcm3[89]|mm10|mm39|chm13|t2t|hs1|gencode|ensembl|refseq|ucsc|gatk[_-]?bundle|genome_ref|igenomes)([^a-z0-9]|$)`)
	reNFHash2   = regexp.MustCompile(`^[0-9a-f]{2}$`)
	reNFHash30  = regexp.MustCompile(`^[0-9a-f]{30}$`)
	reTempDir   = regexp.MustCompile(`(?i)^(\.snakemake|__pycache__|\.cache|\.nextflow|tmp|temp|\.tmp|_tmp|scratch|\.ipynb_checkpoints|\.pytest_cache|\.mypy_cache|_temporary|\.gradle|\.npm|\.cargo|\.rustup)$`)
	reSoftware  = regexp.MustCompile(`(?i)^(\.?conda|\.?miniconda\d?|\.?anaconda\d?|\.?miniforge\d?|\.?mambaforge|\.?micromamba|\.condarc\.d|\.singularity|\.apptainer|site-packages|\.local|R|renv|\.julia|node_modules|\.venv|venv|\.pixi)$`)
	reTrashDir  = regexp.MustCompile(`(?i)^(\.trash(-\d+)?|\.trashes|trash|\$recycle\.bin|old|backup|backups|\.backup)$`)
	reTrashFile = regexp.MustCompile(`(?i)(\.bak|\.old|\.orig|~|\.swp)$`)
)

// DirContext is the class inherited by everything below a directory.
// parentClass is the class of the parent directory; parentName/grandName are
// the names of the parent and grandparent (for Nextflow work/xx/hash detection).
func DirContext(name, parentName, grandName string, parentClass Class) Class {
	// Stronger, more specific signals win over inherited ones, except that
	// anything under a temp/trash area stays temp/trash.
	if parentClass == ClassTemp || parentClass == ClassTrash {
		return parentClass
	}
	if reTempDir.MatchString(name) {
		return ClassTemp
	}
	// A bare "work" dir is not flagged by itself (too common a name); Nextflow
	// task dirs work/xx/<30 hex> are.
	if reNFHash30.MatchString(name) && reNFHash2.MatchString(parentName) && grandName == "work" {
		return ClassTemp
	}
	if reTrashDir.MatchString(name) {
		return ClassTrash
	}
	if parentClass != ClassNone {
		return parentClass
	}
	if reSoftware.MatchString(name) {
		return ClassSoftware
	}
	if reAccession.MatchString(name) {
		return ClassPublic
	}
	if reRefGenome.MatchString(name) {
		return ClassRefGenome
	}
	return ClassNone
}

// FileClass classifies a file given the class of its directory.
func FileClass(name string, cat Cat, dirClass Class) Class {
	if dirClass == ClassTemp || dirClass == ClassTrash || dirClass == ClassSoftware {
		return dirClass
	}
	if cat == CatCoreDump {
		return ClassTemp
	}
	if reTrashFile.MatchString(name) {
		return ClassTrash
	}
	if cat == CatSra {
		return ClassPublic
	}
	if dirClass != ClassNone {
		return dirClass
	}
	if cat == CatImage && (strings.HasSuffix(name, ".sif") || strings.HasSuffix(name, ".simg")) {
		return ClassSoftware
	}
	if reAccession.MatchString(name) {
		return ClassPublic
	}
	if reRefGenome.MatchString(name) {
		return ClassRefGenome
	}
	return ClassNone
}

// ExtractAccession returns the first archive accession found in s, or "".
func ExtractAccession(s string) string {
	m := reAccession.FindStringSubmatch(s)
	if m == nil {
		return ""
	}
	return m[2]
}
