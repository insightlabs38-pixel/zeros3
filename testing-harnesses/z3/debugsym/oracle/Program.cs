using System.Reflection.Metadata;
using System.Reflection.Metadata.Ecma335;
using System.Reflection.PortableExecutable;
using System.Security.Cryptography;
using System.Text.Json;
// Independent .NET reader: semantics are transient evidence, never decoder state.
if (args.Length != 2) throw new Exception("usage: oracle assembly.dll symbols.pdb");
using var pe = new PEReader(File.OpenRead(args[0]));
var asm = pe.GetMetadataReader();
using var provider = MetadataReaderProvider.FromPortablePdbStream(File.OpenRead(args[1]));
var pdb = provider.GetMetadataReader();
var methods = new SortedDictionary<string,object>();
foreach (var h in asm.MethodDefinitions) {
 var m=asm.GetMethodDefinition(h); var type=asm.GetTypeDefinition(m.GetDeclaringType());
 var key=asm.GetString(type.Namespace)+"."+asm.GetString(type.Name)+"::"+asm.GetString(m.Name)+":"+Convert.ToHexString(asm.GetBlobBytes(m.Signature));
 var d=pdb.GetMethodDebugInformation(h);var points=d.GetSequencePoints().ToArray();
 methods.Add(key,new { token=MetadataTokens.GetToken(h),sequence_blob=MetadataTokens.GetHeapOffset(d.SequencePointsBlob),sequence_bytes=pdb.GetBlobBytes(d.SequencePointsBlob).Length,points=points.Length,visible=points.Count(x=>!x.IsHidden),scopes=pdb.GetLocalScopes(h).Count,documents=points.Where(x=>!x.Document.IsNil).Select(x=>pdb.GetString(pdb.GetDocument(x.Document).Name)).Distinct().Order().ToArray() });
}
var rows=new SortedDictionary<string,int>();
foreach(var name in new[]{TableIndex.Document,TableIndex.MethodDebugInformation,TableIndex.LocalScope,TableIndex.LocalVariable,TableIndex.LocalConstant,TableIndex.ImportScope,TableIndex.StateMachineMethod,TableIndex.CustomDebugInformation}) rows.Add(name.ToString(),pdb.GetTableRowCount(name));
Console.WriteLine(JsonSerializer.Serialize(new{rows,methods,pdb_sha256=Convert.ToHexString(SHA256.HashData(File.ReadAllBytes(args[1]))).ToLowerInvariant()}));
