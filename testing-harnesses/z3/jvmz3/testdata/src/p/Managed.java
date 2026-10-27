package p;

import java.lang.annotation.Retention;
import java.lang.annotation.RetentionPolicy;
import java.lang.invoke.MethodHandle;
import java.lang.invoke.MethodHandles;
import java.lang.invoke.MethodType;
import java.util.ArrayList;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.function.IntSupplier;

@Retention(RetentionPolicy.RUNTIME)
@interface Tag {
    String value() default "tag";
    int[] nums() default {1, 2, 3};
    Class<?> type() default Object.class;
    Kind kind() default Kind.A;
}

enum Kind { A, B, C }

interface Shape {
    double area();
    default String describe() { return "shape:" + area(); }
}

abstract class Base implements Shape {
    protected final int id;
    static final long SERIAL = 0x1122334455667788L;
    static final double SCALE = 2.5d;
    static final float RATIO = 0.75f;
    static final String NAME = "base";
    static int[] TABLE = {3, 1, 4, 1, 5, 9, 2, 6, 5, 3, 5};
    static long[] WIDE = {1L << 40, 2L, 3L};
    Base(int id) { this.id = id; }
    abstract int weight();
}

@Tag(value = "managed", nums = {7, 8}, type = String.class, kind = Kind.B)
public class Managed extends Base implements IntSupplier {
    private int count;
    private long total;
    private String label = "label";
    private final List<String> names = new ArrayList<>();
    private final Map<String, Integer> index = new HashMap<>();

    public Managed(int id) { super(id); }

    @Override public double area() { return id * SCALE; }
    @Override int weight() { return count + (int) total; }
    @Override public int getAsInt() { return count; }

    public int packed(int x) {
        switch (x) {
            case 1: return 10;
            case 2: return 20;
            case 3: return 30;
            case 4: return 40;
            default: return -1;
        }
    }

    public int sparse(int x) {
        switch (x) {
            case 10: return 1;
            case 1000: return 2;
            case 100000: return 3;
            case -7: return 4;
            default: return 0;
        }
    }

    public String strSwitch(String s) {
        switch (s) {
            case "alpha": return "a";
            case "beta": return "b";
            default: return "?";
        }
    }

    public int tries(String s) {
        int r = 0;
        try {
            r = Integer.parseInt(s);
        } catch (NumberFormatException e) {
            r = -1;
        } catch (RuntimeException e) {
            r = -2;
        } finally {
            count++;
        }
        return r;
    }

    public long wide(long a, double b) {
        long r = a * 3 + (long) b;
        for (int i = 0; i < 1000; i += 300) {
            r += i;
        }
        total += r;
        return r;
    }

    public int[][] matrix(int n) { return new int[n][3]; }

    public Object cast(Object o) {
        if (o instanceof String) { return ((String) o).length(); }
        if (o instanceof Integer) { return (Integer) o + 1; }
        return null;
    }

    public Object poly() throws Throwable {
        MethodHandle mh = MethodHandles.lookup().findVirtual(String.class, "length", MethodType.methodType(int.class));
        return (int) mh.invoke("poly");
    }

    public int lambda(int k) {
        IntSupplier s = () -> k * 2;
        Runnable r = () -> count++;
        r.run();
        return s.getAsInt() + ("x" + k + label).length();
    }

    public String many() {
        StringBuilder sb = new StringBuilder();
        sb.append("c000").append("c001").append("c002").append("c003").append("c004").append("c005").append("c006").append("c007");
        sb.append("c008").append("c009").append("c010").append("c011").append("c012").append("c013").append("c014").append("c015");
        sb.append("c016").append("c017").append("c018").append("c019").append("c020").append("c021").append("c022").append("c023");
        sb.append("c024").append("c025").append("c026").append("c027").append("c028").append("c029").append("c030").append("c031");
        sb.append("c032").append("c033").append("c034").append("c035").append("c036").append("c037").append("c038").append("c039");
        sb.append("c040").append("c041").append("c042").append("c043").append("c044").append("c045").append("c046").append("c047");
        sb.append("c048").append("c049").append("c050").append("c051").append("c052").append("c053").append("c054").append("c055");
        sb.append("c056").append("c057").append("c058").append("c059").append("c060").append("c061").append("c062").append("c063");
        sb.append("c064").append("c065").append("c066").append("c067").append("c068").append("c069").append("c070").append("c071");
        sb.append("c072").append("c073").append("c074").append("c075").append("c076").append("c077").append("c078").append("c079");
        sb.append("c080").append("c081").append("c082").append("c083").append("c084").append("c085").append("c086").append("c087");
        sb.append("c088").append("c089").append("c090").append("c091").append("c092").append("c093").append("c094").append("c095");
        sb.append("c096").append("c097").append("c098").append("c099").append("c100").append("c101").append("c102").append("c103");
        sb.append("c104").append("c105").append("c106").append("c107").append("c108").append("c109").append("c110").append("c111");
        sb.append("c112").append("c113").append("c114").append("c115").append("c116").append("c117").append("c118").append("c119");
        sb.append("c120").append("c121").append("c122").append("c123").append("c124").append("c125").append("c126").append("c127");
        sb.append("c128").append("c129").append("c130").append("c131").append("c132").append("c133").append("c134").append("c135");
        sb.append("c136").append("c137").append("c138").append("c139").append("c140").append("c141").append("c142").append("c143");
        sb.append("c144").append("c145").append("c146").append("c147").append("c148").append("c149").append("c150").append("c151");
        sb.append("c152").append("c153").append("c154").append("c155").append("c156").append("c157").append("c158").append("c159");
        sb.append("c160").append("c161").append("c162").append("c163").append("c164").append("c165").append("c166").append("c167");
        sb.append("c168").append("c169").append("c170").append("c171").append("c172").append("c173").append("c174").append("c175");
        sb.append("c176").append("c177").append("c178").append("c179").append("c180").append("c181").append("c182").append("c183");
        sb.append("c184").append("c185").append("c186").append("c187").append("c188").append("c189").append("c190").append("c191");
        sb.append("c192").append("c193").append("c194").append("c195").append("c196").append("c197").append("c198").append("c199");
        sb.append("c200").append("c201").append("c202").append("c203").append("c204").append("c205").append("c206").append("c207");
        sb.append("c208").append("c209").append("c210").append("c211").append("c212").append("c213").append("c214").append("c215");
        sb.append("c216").append("c217").append("c218").append("c219").append("c220").append("c221").append("c222").append("c223");
        sb.append("c224").append("c225").append("c226").append("c227").append("c228").append("c229").append("c230").append("c231");
        sb.append("c232").append("c233").append("c234").append("c235").append("c236").append("c237").append("c238").append("c239");
        sb.append("c240").append("c241").append("c242").append("c243").append("c244").append("c245").append("c246").append("c247");
        sb.append("c248").append("c249").append("c250").append("c251").append("c252").append("c253").append("c254").append("c255");
        sb.append("c256").append("c257").append("c258").append("c259").append("c260");
        index.put("many", 1);
        names.add(sb.toString());
        return sb.toString();
    }

    @Deprecated
    public <T extends Comparable<T>> T max(List<? extends T> in, @Tag("p") T seed) throws java.io.IOException {
        T best = seed;
        for (T t : in) {
            if (t.compareTo(best) > 0) best = t;
        }
        return best;
    }

    public Runnable anon(final int n) {
        return new Runnable() {
            @Override public void run() { count += n; }
        };
    }

    static class Nested {
        int v = 7;
        class Inner { int w = v + 1; }
    }
}
