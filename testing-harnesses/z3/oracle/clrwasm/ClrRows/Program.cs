// ORACLE (Z3-33): the .NET runtime's own metadata reader reports, for each assembly given, the row
// count of every metadata table, the heap sizes, the number of method bodies, their IL byte total,
// exception-region total and local-signature-token total. `z3clr oracle` compares these numbers
// with clrz3's independent parse. Nothing here is used by the representation or its decoder.
using System.Reflection.Metadata;
using System.Reflection.Metadata.Ecma335;
using System.Reflection.PortableExecutable;

foreach (var path in args)
{
    using var fs = File.OpenRead(path);
    using var pe = new PEReader(fs);
    if (!pe.HasMetadata) { Console.WriteLine($"{path}\tnometadata"); continue; }
    var md = pe.GetMetadataReader();
    var parts = new List<string>();
    for (int i = 0; i < 64; i++)
    {
        var t = (TableIndex)i;
        if (!Enum.IsDefined(t)) continue;
        int n = md.GetTableRowCount(t);
        if (n > 0) parts.Add($"{(int)t}={n}");
    }
    long il = 0, eh = 0, loc = 0; int bodies = 0, fat = 0;
    foreach (var h in md.MethodDefinitions)
    {
        var m = md.GetMethodDefinition(h);
        if (m.RelativeVirtualAddress == 0) continue;
        var b = pe.GetMethodBody(m.RelativeVirtualAddress);
        bodies++;
        il += b.GetILBytes()!.Length;
        eh += b.ExceptionRegions.Length;
        var tok = b.LocalSignature.IsNil ? 0 : MetadataTokens.GetToken(b.LocalSignature);
        loc += tok & 0xffffff;
        if (b.LocalSignature.IsNil == false) fat++;
    }
    Console.WriteLine($"{path}\t{string.Join(",", parts)}\tstr={md.GetHeapSize(HeapIndex.String)} blob={md.GetHeapSize(HeapIndex.Blob)} us={md.GetHeapSize(HeapIndex.UserString)} guid={md.GetHeapSize(HeapIndex.Guid)}\tbodies={bodies} il={il} eh={eh} locals={loc} withLocals={fat}");
}
