package clrz3

// ECMA-335 II.22 metadata table schema, finite and declarative: every table of an ordinary
// assembly (0x00..0x2C) with its columns, the coded-index families (II.24.2.6) and the blob
// signature family each column carries. Nothing here is reflective; a table the schema does not
// know makes the file unsupported.

const (
	TModule = iota
	TTypeRef
	TTypeDef
	TFieldPtr
	TField
	TMethodPtr
	TMethodDef
	TParamPtr
	TParam
	TInterfaceImpl
	TMemberRef
	TConstant
	TCustomAttribute
	TFieldMarshal
	TDeclSecurity
	TClassLayout
	TFieldLayout
	TStandAloneSig
	TEventMap
	TEventPtr
	TEvent
	TPropertyMap
	TPropertyPtr
	TProperty
	TMethodSemantics
	TMethodImpl
	TModuleRef
	TTypeSpec
	TImplMap
	TFieldRVA
	TEncLog
	TEncMap
	TAssembly
	TAssemblyProcessor
	TAssemblyOS
	TAssemblyRef
	TAssemblyRefProcessor
	TAssemblyRefOS
	TFile
	TExportedType
	TManifestResource
	TNestedClass
	TGenericParam
	TMethodSpec
	TGenericParamConstraint
	NumTables
)

var tableNames = [NumTables]string{"Module", "TypeRef", "TypeDef", "FieldPtr", "Field", "MethodPtr", "MethodDef", "ParamPtr", "Param",
	"InterfaceImpl", "MemberRef", "Constant", "CustomAttribute", "FieldMarshal", "DeclSecurity", "ClassLayout", "FieldLayout",
	"StandAloneSig", "EventMap", "EventPtr", "Event", "PropertyMap", "PropertyPtr", "Property", "MethodSemantics", "MethodImpl",
	"ModuleRef", "TypeSpec", "ImplMap", "FieldRVA", "EncLog", "EncMap", "Assembly", "AssemblyProcessor", "AssemblyOS",
	"AssemblyRef", "AssemblyRefProcessor", "AssemblyRefOS", "File", "ExportedType", "ManifestResource", "NestedClass",
	"GenericParam", "MethodSpec", "GenericParamConstraint"}

// TableName is the ECMA name of table t.
func TableName(t int) string {
	if t >= 0 && t < NumTables {
		return tableNames[t]
	}
	return "?"
}

// Coded-index families.
const (
	CTypeDefOrRef = iota
	CHasConstant
	CHasCustomAttribute
	CHasFieldMarshal
	CHasDeclSecurity
	CMemberRefParent
	CHasSemantics
	CMethodDefOrRef
	CMemberForwarded
	CImplementation
	CCustomAttributeType
	CResolutionScope
	CTypeOrMethodDef
	NumCoded
)

type codedDef struct {
	bits int
	tabs []int // -1: unused tag
}

var coded = [NumCoded]codedDef{
	CTypeDefOrRef:        {2, []int{TTypeDef, TTypeRef, TTypeSpec}},
	CHasConstant:         {2, []int{TField, TParam, TProperty}},
	CHasCustomAttribute:  {5, []int{TMethodDef, TField, TTypeRef, TTypeDef, TParam, TInterfaceImpl, TMemberRef, TModule, TDeclSecurity, TProperty, TEvent, TStandAloneSig, TModuleRef, TTypeSpec, TAssembly, TAssemblyRef, TFile, TExportedType, TManifestResource, TGenericParam, TGenericParamConstraint, TMethodSpec}},
	CHasFieldMarshal:     {1, []int{TField, TParam}},
	CHasDeclSecurity:     {2, []int{TTypeDef, TMethodDef, TAssembly}},
	CMemberRefParent:     {3, []int{TTypeDef, TTypeRef, TModuleRef, TMethodDef, TTypeSpec}},
	CHasSemantics:        {1, []int{TEvent, TProperty}},
	CMethodDefOrRef:      {1, []int{TMethodDef, TMemberRef}},
	CMemberForwarded:     {1, []int{TField, TMethodDef}},
	CImplementation:      {2, []int{TFile, TAssemblyRef, TExportedType}},
	CCustomAttributeType: {3, []int{-1, -1, TMethodDef, TMemberRef, -1}},
	CResolutionScope:     {2, []int{TModule, TModuleRef, TAssemblyRef, TTypeRef}},
	CTypeOrMethodDef:     {1, []int{TTypeDef, TMethodDef}},
}

// CodedTables lists the target tables of a coded-index family (tag order, -1 = unused).
func CodedTables(c int) []int { return coded[c].tabs }

// CodedBits is the tag width of a coded-index family.
func CodedBits(c int) int { return coded[c].bits }

// ColKind is the semantic class of one table column.
type ColKind uint8

const (
	KConst ColKind = iota // fixed-width scalar: flags, versions, sizes
	KStr                  // #Strings offset
	KGuid                 // #GUID ordinal
	KBlob                 // #Blob offset
	KTable                // simple index into one table
	KCoded                // coded index
	KList                 // list-start index into a child table (layout)
	KRVA                  // method / field RVA (layout)
)

// Sig is the signature family a blob column carries.
type Sig uint8

const (
	SigNone   Sig = iota // opaque blob
	SigAny               // method or field signature (MemberRef) / local, method or field (StandAloneSig)
	SigMethod            // MethodDefSig
	SigField             // FieldSig
	SigProp              // PropertySig
	SigType              // TypeSpec blob
	SigInst              // MethodSpec instantiation
)

// ColDef describes one column. Key columns define the semantic identity of the row; the other
// columns are resolved after every identity exists.
type ColDef struct {
	Name string
	K    ColKind
	W    int // KConst width
	T    int // KTable / KList target table, KCoded family
	Key  bool
	Sig  Sig
}

func cc(n string, w int) ColDef           { return ColDef{Name: n, K: KConst, W: w} }
func cs(n string, key bool) ColDef        { return ColDef{Name: n, K: KStr, Key: key} }
func cg(n string) ColDef                  { return ColDef{Name: n, K: KGuid} }
func cb(n string, key bool, s Sig) ColDef { return ColDef{Name: n, K: KBlob, Key: key, Sig: s} }
func ct(n string, t int, key bool) ColDef { return ColDef{Name: n, K: KTable, T: t, Key: key} }
func cd(n string, c int, key bool) ColDef { return ColDef{Name: n, K: KCoded, T: c, Key: key} }
func cl(n string, t int) ColDef           { return ColDef{Name: n, K: KList, T: t} }
func cr(n string) ColDef                  { return ColDef{Name: n, K: KRVA, W: 4} }

var schema = [NumTables][]ColDef{
	TModule:          {cc("Generation", 2), cs("Name", true), cg("Mvid"), cg("EncId"), cg("EncBaseId")},
	TTypeRef:         {cd("ResolutionScope", CResolutionScope, true), cs("TypeName", true), cs("TypeNamespace", true)},
	TTypeDef:         {cc("Flags", 4), cs("TypeName", true), cs("TypeNamespace", true), cd("Extends", CTypeDefOrRef, false), cl("FieldList", TField), cl("MethodList", TMethodDef)},
	TFieldPtr:        {ct("Field", TField, false)},
	TField:           {cc("Flags", 2), cs("Name", true), cb("Signature", true, SigField)},
	TMethodPtr:       {ct("Method", TMethodDef, false)},
	TMethodDef:       {cr("RVA"), cc("ImplFlags", 2), cc("Flags", 2), cs("Name", true), cb("Signature", true, SigMethod), cl("ParamList", TParam)},
	TParamPtr:        {ct("Param", TParam, false)},
	TParam:           {cc("Flags", 2), cc("Sequence", 2), cs("Name", false)},
	TInterfaceImpl:   {ct("Class", TTypeDef, true), cd("Interface", CTypeDefOrRef, true)},
	TMemberRef:       {cd("Class", CMemberRefParent, true), cs("Name", true), cb("Signature", true, SigAny)},
	TConstant:        {cc("Type", 2), cd("Parent", CHasConstant, false), cb("Value", false, SigNone)},
	TCustomAttribute: {cd("Parent", CHasCustomAttribute, false), cd("Type", CCustomAttributeType, false), cb("Value", false, SigNone)},
	TFieldMarshal:    {cd("Parent", CHasFieldMarshal, false), cb("NativeType", false, SigNone)},
	TDeclSecurity:    {cc("Action", 2), cd("Parent", CHasDeclSecurity, true), cb("PermissionSet", true, SigNone)},
	TClassLayout:     {cc("PackingSize", 2), cc("ClassSize", 4), ct("Parent", TTypeDef, false)},
	TFieldLayout:     {cc("Offset", 4), ct("Field", TField, false)},
	TStandAloneSig:   {cb("Signature", true, SigAny)},
	TEventMap:        {ct("Parent", TTypeDef, true), cl("EventList", TEvent)},
	TEventPtr:        {ct("Event", TEvent, false)},
	TEvent:           {cc("EventFlags", 2), cs("Name", true), cd("EventType", CTypeDefOrRef, true)},
	TPropertyMap:     {ct("Parent", TTypeDef, true), cl("PropertyList", TProperty)},
	TPropertyPtr:     {ct("Property", TProperty, false)},
	TProperty:        {cc("Flags", 2), cs("Name", true), cb("Type", true, SigProp)},
	TMethodSemantics: {cc("Semantics", 2), ct("Method", TMethodDef, false), cd("Association", CHasSemantics, false)},
	TMethodImpl:      {ct("Class", TTypeDef, false), cd("MethodBody", CMethodDefOrRef, false), cd("MethodDeclaration", CMethodDefOrRef, false)},
	TModuleRef:       {cs("Name", true)},
	TTypeSpec:        {cb("Signature", true, SigType)},
	TImplMap:         {cc("MappingFlags", 2), cd("MemberForwarded", CMemberForwarded, false), cs("ImportName", false), ct("ImportScope", TModuleRef, false)},
	TFieldRVA:        {cr("RVA"), ct("Field", TField, false)},
	TEncLog:          {cc("Token", 4), cc("FuncCode", 4)},
	TEncMap:          {cc("Token", 4)},
	TAssembly: {cc("HashAlgId", 4), cc("MajorVersion", 2), cc("MinorVersion", 2), cc("BuildNumber", 2), cc("RevisionNumber", 2), cc("Flags", 4),
		cb("PublicKey", true, SigNone), cs("Name", true), cs("Culture", true)},
	TAssemblyProcessor: {cc("Processor", 4)},
	TAssemblyOS:        {cc("OSPlatformID", 4), cc("OSMajorVersion", 4), cc("OSMinorVersion", 4)},
	TAssemblyRef: {cc("MajorVersion", 2), cc("MinorVersion", 2), cc("BuildNumber", 2), cc("RevisionNumber", 2), cc("Flags", 4),
		cb("PublicKeyOrToken", true, SigNone), cs("Name", true), cs("Culture", true), cb("HashValue", false, SigNone)},
	TAssemblyRefProcessor:   {cc("Processor", 4), ct("AssemblyRef", TAssemblyRef, false)},
	TAssemblyRefOS:          {cc("OSPlatformId", 4), cc("OSMajorVersion", 4), cc("OSMinorVersion", 4), ct("AssemblyRef", TAssemblyRef, false)},
	TFile:                   {cc("Flags", 4), cs("Name", true), cb("HashValue", false, SigNone)},
	TExportedType:           {cc("Flags", 4), cc("TypeDefId", 4), cs("TypeName", true), cs("TypeNamespace", true), cd("Implementation", CImplementation, true)},
	TManifestResource:       {cr("Offset"), cc("Flags", 4), cs("Name", true), cd("Implementation", CImplementation, true)},
	TNestedClass:            {ct("NestedClass", TTypeDef, false), ct("EnclosingClass", TTypeDef, false)},
	TGenericParam:           {cc("Number", 2), cc("Flags", 2), cd("Owner", CTypeOrMethodDef, true), cs("Name", false)},
	TMethodSpec:             {cd("Method", CMethodDefOrRef, true), cb("Instantiation", true, SigInst)},
	TGenericParamConstraint: {ct("Owner", TGenericParam, true), cd("Constraint", CTypeDefOrRef, true)},
}

// Columns returns the column definitions of table t.
func Columns(t int) []ColDef { return schema[t] }

// isPtr reports the indirection tables of the uncompressed (#-) layout.
func isPtr(t int) bool {
	switch t {
	case TFieldPtr, TMethodPtr, TParamPtr, TEventPtr, TPropertyPtr:
		return true
	}
	return false
}
