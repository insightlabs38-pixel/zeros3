package p;

public record Rec(int a, String b) {
    public Rec { if (a < 0) throw new IllegalArgumentException("neg"); }
    public sealed interface S permits Sa, Sb {}
    public record Sa(int x) implements S {}
    public record Sb(long y) implements S {}
    public static String show(S s) {
        return switch (s) {
            case Sa a -> "a" + a.x();
            case Sb b -> "b" + b.y();
        };
    }
}
