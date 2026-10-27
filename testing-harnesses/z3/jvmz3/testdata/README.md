Test fixtures (PRODUCER_WORKLOAD, committed because they are tiny).

`src/` is a small Java program written for Z3-32 to exercise constant-pool tags, bytecode forms
(tableswitch, lookupswitch, wide, invokedynamic, invokeinterface, multianewarray, ldc/ldc_w/ldc2_w),
attributes (Signature, Exceptions, InnerClasses, EnclosingMethod, NestHost/NestMembers,
PermittedSubclasses, Record, BootstrapMethods, ConstantValue, MethodParameters, annotations,
StackMapTable, LocalVariable(Type)Table) and `module-info`.

    javac --release 21 -g -parameters -d classes $(find src -name '*.java')     # OpenJDK 21.0.12

`../dexz3/testdata/fixture-api{24,28}.dex` are D8 8.7.18 output (`--debug --min-api 24|28`, DEX 037|039) for the
non-record classes of the same program (`Base Kind Managed Managed$1 Managed$Nested Managed$Nested$Inner Shape Tag`).
